package postgres

import (
	"strings"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestChallengeRequestValidation(t *testing.T) {
	key := make([]byte, 32)
	cases := []struct {
		name    string
		request *pb.CreateChallengeRequest
		valid   bool
	}{
		{"registration", &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: key, DevicePublicKey: key}, true},
		{"recovery_registration", &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: key, DevicePublicKey: key, Recovery: true}, true},
		{"administrative_registration", &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: key, DevicePublicKey: key, Administrative: true}, true},
		{"login", &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: "grant"}, true},
		{"revoke", &pb.CreateChallengeRequest{Purpose: "device.revoke", GrantId: "grant", RootPublicKey: key}, true},
		{"unknown_purpose", &pb.CreateChallengeRequest{Purpose: "unknown"}, false},
		{"missing_root", &pb.CreateChallengeRequest{Purpose: "device.register", DevicePublicKey: key}, false},
		{"missing_device", &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: key}, false},
		{"registration_with_grant", &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: key, DevicePublicKey: key, GrantId: "grant"}, false},
		{"missing_grant", &pb.CreateChallengeRequest{Purpose: "auth.login"}, false},
		{"long_grant", &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: strings.Repeat("x", 129)}, false},
		{"login_with_device", &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: "grant", DevicePublicKey: key}, false},
		{"login_with_root", &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: "grant", RootPublicKey: key}, false},
		{"login_with_recovery", &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: "grant", Recovery: true}, false},
		{"login_with_administration", &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: "grant", Administrative: true}, false},
		{"revoke_without_root", &pb.CreateChallengeRequest{Purpose: "device.revoke", GrantId: "grant"}, false},
		{"unexpected_recovery_grant", &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: key, DevicePublicKey: key, RecoveryGrantId: "grant"}, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := validateChallengeRequest(item.request)
			if (err == nil) != item.valid {
				t.Fatalf("valid=%t error=%v", item.valid, err)
			}
			if err != nil && status.Code(err) != codes.InvalidArgument {
				t.Fatalf("wrong status: %v", err)
			}
		})
	}
}

func TestPairingPurposeValidation(t *testing.T) {
	cases := []struct {
		purpose         string
		recovery, valid bool
	}{
		{"device.register", false, true}, {"device.delegate", false, true},
		{"device.register", true, false}, {"auth.login", false, false},
		{"recovery.device.revoke", false, false},
	}
	for _, item := range cases {
		request := &pb.CreateChallengeRequest{Purpose: item.purpose, Recovery: item.recovery, PairingId: "pair"}
		if err := validatePairingPurpose(request); (err == nil) != item.valid {
			t.Fatalf("purpose=%s recovery=%t error=%v", item.purpose, item.recovery, err)
		}
	}
}
