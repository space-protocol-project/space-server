package postgres

import (
	"testing"

	"google.golang.org/grpc/codes"
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
