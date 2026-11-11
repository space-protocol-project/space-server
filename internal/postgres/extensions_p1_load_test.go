package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type loadSample struct {
	Phase        int
	Client       int
	Case         string
	Code         string
	Milliseconds float64
	Warmup       bool
}
type loadMetrics struct {
	sync.Mutex
	samples   []loadSample
	committed atomic.Int64
}

// Только явный профиль CI. Длительность и квоты не настраиваются переменными.
func TestExtensionP1Load(t *testing.T) {
	if os.Getenv("SPACE_P1_LOAD") != "1" {
		t.Skip("Отдельная 15-минутная приёмка CI")
	}
	f := newChannelFixtureTimeout(t, 19*time.Minute)
	channel := f.create(t, "load_main")
	independent := f.create(t, "load_other")
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	module, err := os.ReadFile(os.Getenv("SPACE_P1_MUTATION_MODULE"))
	if err != nil {
		t.Fatal(err)
	}
	bindings := mutationBindings(channel.Id, module)
	for phase := 0; phase < 3; phase++ {
		for client := 0; client < 8; client++ {
			for slot := 0; slot < 15; slot++ {
				bindings[loadInstance(phase, client, slot)] = bindings["demo"]
			}
		}
	}
	other := bindings["demo"]
	other.Channel = independent.Id
	bindings["other"] = other
	bindings["quota"] = bindings["demo"]
	service, err := f.store.NewExtensionP1MutationService(f.ctx, host, bindings)
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1RPCClient(t, f, service)
	out := os.Getenv("SPACE_P1_LOAD_OUTPUT")
	if out == "" {
		t.Fatal("Нет каталога результатов")
	}
	if err = os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	metrics := &loadMetrics{}
	loadAccess(t, f, api, channel)
	loadQuota(t, f, api)
	resources, err := os.Create(filepath.Join(out, "resources.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Close()
	for phase, clients := range []int{1, 2, 8} {
		start := time.Now()
		deadline := start.Add(5 * time.Minute)
		var wg sync.WaitGroup
		for client := 0; client < clients; client++ {
			wg.Add(1)
			go func(client int) { defer wg.Done(); loadClient(t, f, api, metrics, phase, client, start, deadline) }(client)
		}
		for time.Now().Before(deadline) {
			if time.Since(start) >= 30*time.Second && time.Since(start) < 35*time.Second {
				loadRevokeDuringTraffic(t, f, api, channel)
			}
			loadResource(t, f, host, resources, phase, start)
			time.Sleep(min(5*time.Second, time.Until(deadline)))
		}
		wg.Wait()
		loadResource(t, f, host, resources, phase, start)
	}
	stats := host.Stats()
	if stats.Active != 0 || stats.ReservedBytes != 0 || stats.OSProcesses != 0 {
		t.Fatal("Ресурсы не освобождены", stats)
	}
	entries, err := os.ReadDir(os.Getenv("SPACE_P1_CGROUP"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Error("Осталась группа вызова", entry.Name())
		}
	}
	var data, receipts, events, revisions int64
	err = f.store.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM extension_p1_data), (SELECT count(*) FROM extension_p1_receipts WHERE instance<>'quota'),(SELECT count(*) FROM extension_p1_outbox),(SELECT COALESCE(sum(revision),0) FROM extension_p1_instances)`).Scan(&data, &receipts, &events, &revisions)
	want := metrics.committed.Load() + 1 // Контрольная независимая команда перед нагрузкой.
	if err != nil || data != want || receipts != want || events != want || revisions != want {
		t.Fatal("Частичная запись или дубликат", data, receipts, events, revisions, want, err)
	}
	control := mutationRequest("load-final-control", 0)
	if _, err = api.ExecuteExtensionCommand(f.owner, control); err != nil {
		t.Fatal(err)
	}
	loadWrite(t, out, metrics)
	if pool := f.store.pool.Stat(); pool.AcquiredConns() != 0 || pool.TotalConns() > pool.MaxConns() {
		t.Fatal("Пул не освобождён", pool)
	}
}

func loadRevokeDuringTraffic(t *testing.T, f *channelFixture, api pb.ExtensionP1ServiceClient, c *pb.Channel) {
	member, id := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	_, err := api.QueryExtension(member, extensionP1Query())
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	var revision int64
	if err = f.store.pool.QueryRow(f.ctx, "SELECT revision FROM memberships WHERE principal_id=$1", id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", Blocked: true, ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.QueryExtension(member, extensionP1Query())
	requireCode(t, err, codes.PermissionDenied)
	t.Log("load: ACL and membership revoked during traffic; both denied before WASM")
}

func loadInstance(phase, client, slot int) string {
	return fmt.Sprintf("load_%d_%d_%d", phase, client, slot)
}

func loadClient(t *testing.T, f *channelFixture, api pb.ExtensionP1ServiceClient, m *loadMetrics, phase, client int, start, deadline time.Time) {
	confirmed := map[int]bool{}
	for sequence := 0; time.Now().Before(deadline); sequence++ {
		slot := min(int(time.Since(start)/(20*time.Second)), 14)
		instance := loadInstance(phase, client, slot)
		r := mutationRequest("load-command-0001", 0)
		r.InstanceId = instance
		kind := sequence % 8
		if kind == 3 && !confirmed[slot] {
			kind = 1
		}
		before := time.Now()
		name, err := loadOperation(f, api, r, kind, m)
		code := status.Code(err)
		if (kind == 1 || kind == 2) && err == nil {
			confirmed[slot] = true
		}
		if !loadExpected(kind, code) {
			t.Errorf("phase=%d client=%d case=%s unexpected=%v", phase, client, name, err)
			return
		}
		m.Lock()
		m.samples = append(m.samples, loadSample{phase, client, name, code.String(), float64(time.Since(before).Microseconds()) / 1000, time.Since(start) < time.Minute})
		m.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
}

func loadOperation(f *channelFixture, api pb.ExtensionP1ServiceClient, r *pb.ExecuteExtensionCommandRequest, kind int, m *loadMetrics) (string, error) {
	switch kind {
	case 0, 7:
		q := extensionP1Query()
		q.InstanceId = r.InstanceId
		if kind == 7 {
			q.InstanceId = "other"
		}
		_, err := api.QueryExtension(f.owner, q)
		return "query", err
	case 3:
		r.PayloadJson = []byte(`{ }`)
		_, err := api.ExecuteExtensionCommand(f.owner, r)
		return "payload_conflict", err
	case 4:
		r.IdempotencyKey = "load-revision-conflict"
		r.ExpectedStateRevision = 2
		_, err := api.ExecuteExtensionCommand(f.owner, r)
		return "revision_conflict", err
	case 5:
		ctx, cancel := context.WithCancel(f.owner)
		cancel()
		_, err := api.QueryExtension(ctx, extensionP1Query())
		return "cancel", err
	case 6:
		r.Effect = pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_NONE
		r.IdempotencyKey = ""
		_, err := api.ExecuteExtensionCommand(f.owner, r)
		return "stateless", err
	default:
		response, err := api.ExecuteExtensionCommand(f.owner, r)
		if err == nil && !response.Replayed {
			m.committed.Add(1)
		}
		name := "unique_or_retry"
		if kind == 2 {
			name = "replay"
		}
		return name, err
	}
}

func loadExpected(kind int, code codes.Code) bool {
	if kind == 5 {
		return code == codes.Canceled
	}
	if code == codes.ResourceExhausted {
		return true
	}
	if kind == 3 {
		return code == codes.AlreadyExists
	}
	if kind == 4 {
		return code == codes.Aborted
	}
	return code == codes.OK
}

func loadAccess(t *testing.T, f *channelFixture, api pb.ExtensionP1ServiceClient, c *pb.Channel) {
	member, id := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	if _, err := api.QueryExtension(member, extensionP1Query()); err != nil {
		t.Fatal(err)
	}
	f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	_, err := api.QueryExtension(member, extensionP1Query())
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	var revision int64
	if err = f.store.pool.QueryRow(f.ctx, "SELECT revision FROM memberships WHERE principal_id=$1", id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", Blocked: true, ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.QueryExtension(member, extensionP1Query())
	requireCode(t, err, codes.PermissionDenied)
}

func loadQuota(t *testing.T, f *channelFixture, api pb.ExtensionP1ServiceClient) {
	_, err := f.store.pool.Exec(f.ctx, `INSERT INTO extension_p1_receipts SELECT 'install','quota','quota-principal','acknowledge',n::text,decode(repeat('00',32),'hex'),''::bytea,clock_timestamp(),false FROM generate_series(1,1024) n`)
	if err != nil {
		t.Fatal(err)
	}
	q := mutationRequest("load-quota-check", 0)
	q.InstanceId = "quota"
	_, err = api.ExecuteExtensionCommand(f.owner, q)
	requireCode(t, err, codes.ResourceExhausted)
	q = mutationRequest("load-independent", 0)
	q.InstanceId = "other"
	if _, err = api.ExecuteExtensionCommand(f.owner, q); err != nil {
		t.Fatal(err)
	}
}

func loadResource(t *testing.T, f *channelFixture, host *extensions.ProcessHost, file *os.File, phase int, start time.Time) {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	rss := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") || strings.HasPrefix(line, "VmHWM:") {
			rss = append(rss, line)
		}
	}
	pool := f.store.pool.Stat()
	var data, receipts, outbox int64
	err = f.store.pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT sum(octet_length(value)) FROM extension_p1_data),0),COALESCE((SELECT sum(octet_length(response)) FROM extension_p1_receipts),0),COALESCE((SELECT sum(octet_length(payload)) FROM extension_p1_outbox),0)`).Scan(&data, &receipts, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{"phase": phase, "elapsed_seconds": time.Since(start).Seconds(), "host": host.Stats(), "server_memory": rss, "pool_total": pool.TotalConns(), "pool_acquired": pool.AcquiredConns(), "pool_max": pool.MaxConns(), "pool_acquire_duration_ns": pool.AcquireDuration().Nanoseconds(), "pool_empty_acquires": pool.EmptyAcquireCount(), "data_bytes": data, "receipt_bytes": receipts, "outbox_bytes": outbox}
	if err = json.NewEncoder(file).Encode(row); err != nil {
		t.Fatal(err)
	}
}

func loadWrite(t *testing.T, out string, m *loadMetrics) {
	file, err := os.Create(filepath.Join(out, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	groups := map[string][]float64{}
	for _, sample := range m.samples {
		if err = json.NewEncoder(file).Encode(sample); err != nil {
			t.Fatal(err)
		}
		if !sample.Warmup {
			key := fmt.Sprintf("%d/%s/%s", sample.Phase, sample.Case, sample.Code)
			groups[key] = append(groups[key], sample.Milliseconds)
		}
	}
	summary := map[string]any{}
	for key, values := range groups {
		sort.Float64s(values)
		summary[key] = map[string]any{"count": len(values), "requests_per_second": float64(len(values)) / 240, "p50_ms": values[(len(values)-1)*50/100], "p95_ms": values[(len(values)-1)*95/100], "p99_ms": values[(len(values)-1)*99/100]}
	}
	raw, err := json.MarshalIndent(map[string]any{"phases_seconds": []int{300, 300, 300}, "warmup_seconds": 60, "clients": []int{1, 2, 8}, "committed": m.committed.Load(), "distributions": summary}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(out, "summary.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}
