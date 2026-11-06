package postgres

import (
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestExtensionP1DeliveryRegistrySnapshot(t *testing.T) {
	for _, kind := range []string{"revision", "state", "generation"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			h := new(extensionP1Probe)
			s, api := managedFixture(t, f, h)
			managedChange(t, f, s, "enable", 1)
			h.during = func([]byte) {
				query := "UPDATE extension_p1_installations SET revision=revision+2 WHERE installation='install'"
				if kind == "state" {
					query = "UPDATE extension_p1_installations SET state='disabled',revision=revision+1 WHERE installation='install'"
				}
				if kind == "generation" {
					query = "UPDATE extension_p1_installations SET generation=repeat('f',32) WHERE installation='install'"
				}
				// Имитация изменения постоянного реестра другим владельцем/процессом.
				if _, err := f.store.pool.Exec(f.ctx, query); err != nil {
					t.Error(err)
				}
			}
			result, err := api.QueryExtension(f.owner, extensionP1Query())
			requireCode(t, err, codes.FailedPrecondition)
			if result != nil || h.calls.Load() != 1 {
				t.Fatal("Устаревший результат возвращён", result, h.calls.Load())
			}
			h.during = nil
			if kind == "revision" {
				if _, err := api.QueryExtension(f.owner, extensionP1Query()); err != nil {
					t.Fatal("Новый вызов не получил свежий снимок", err)
				}
			} else {
				_, err := api.QueryExtension(f.owner, extensionP1Query())
				requireCode(t, err, codes.FailedPrecondition)
				if h.calls.Load() != 1 {
					t.Fatal("Гость запущен после отсечения установки")
				}
			}
		})
	}
}

func TestExtensionP1DeliveryAuthoritySnapshot(t *testing.T) {
	for _, kind := range []string{"acl-roundtrip", "logout"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "delivery_authority")
			f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
			member, _ := f.enroll(t, true)
			s, err := f.store.NewExtensionP1ManagedService(f.ctx, new(extensionP1Probe), mutationBindings(c.Id, []byte{1}))
			if err != nil {
				t.Fatal(err)
			}
			managedChange(t, f, s, "enable", 1)
			ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(f.ctx, metadata.Pairs("authorization", "Bearer "+lifecycleToken(member))), f.store, "/space.p1.delivery/Query")
			if err != nil {
				t.Fatal(err)
			}
			call, release, err := s.begin(ctx, "demo", false)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := s.checkDelivery(call, "demo"); err != nil {
				t.Fatal(err)
			}
			if kind == "logout" {
				if _, err := f.auth.Logout(member, &pb.LogoutRequest{}); err != nil {
					t.Fatal(err)
				}
			} else {
				f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
				f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
			}
			requireCode(t, s.checkDelivery(call, "demo"), codes.PermissionDenied)
			if kind == "acl-roundtrip" {
				fresh, finish, err := s.begin(ctx, "demo", false)
				if err != nil {
					t.Fatal(err)
				}
				defer finish()
				if err := s.checkDelivery(fresh, "demo"); err != nil {
					t.Fatal("Свежий снимок отклонён", err)
				}
			}
		})
	}
}
