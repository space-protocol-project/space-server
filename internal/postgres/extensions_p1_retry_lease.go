package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
)

const extensionRetryWindow = 30 * time.Second

type extensionRetryLease struct {
	actor, instance, history string
	until                    time.Time
}

// Разрешения живут только в этом исполнителе. Перезапуск закрывает все окна.
// Обслуживание квитанций требует остановки пространства; оно не может удалить
// ответ во время действующего окна этого процесса.
type extensionRetryLeases struct {
	mu      sync.Mutex
	entries map[string]extensionRetryLease
	now     func() time.Time
}

func (l *extensionRetryLeases) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *extensionRetryLeases) issue(actor, instance, history string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if l.entries == nil {
		l.entries = make(map[string]extensionRetryLease)
	}
	count := 0
	for id, item := range l.entries {
		if !now.Before(item.until) {
			delete(l.entries, id)
			continue
		}
		if item.actor == actor {
			count++
		}
	}
	if len(l.entries) >= 128 || count >= 8 {
		return "", mutationError(codes.ResourceExhausted, "retry_window_quota")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	l.entries[id] = extensionRetryLease{actor, instance, history, now.Add(extensionRetryWindow)}
	return id, nil
}

func (l *extensionRetryLeases) check(ctx context.Context, r *pb.ExecuteExtensionCommandRequest) error {
	// Старые прототипы могут исполнять команды без окна, но не объявлять его клиенту.
	if r.RetryLeaseId == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item, ok := l.entries[r.RetryLeaseId]
	if !ok || !l.clock().Before(item.until) || item.actor != authn.Actor(ctx) || item.instance != r.InstanceId || item.history != r.HistoryId {
		return mutationError(codes.FailedPrecondition, "retry_window_closed")
	}
	return nil
}
