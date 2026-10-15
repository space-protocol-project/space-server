package identityrotation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strings"
)

// RetainedDevice фиксирует ключ, права и срок без их продления при смене root.
type RetainedDevice struct {
	ExpiresAt int64    `json:"expires_at"`
	GrantID   string   `json:"grant_id"`
	PublicKey string   `json:"public_key"`
	Scopes    []string `json:"scopes"`
}

type RetentionProof struct {
	Device   RetainedDevice
	Index    int
	Count    int
	Siblings [][]byte
}

func retentionLeaf(device RetainedDevice) []byte {
	raw, _ := json.Marshal(device)
	h := sha256.Sum256(append([]byte("space/root-retained/leaf/v1\x00"), raw...))
	return h[:]
}

func retentionNode(left, right []byte) []byte {
	raw := append([]byte("space/root-retained/node/v1\x00"), left...)
	h := sha256.Sum256(append(raw, right...))
	return h[:]
}

func validRetainedDevice(d RetainedDevice) bool {
	_, validKey := key(d.PublicKey)
	validID := strings.HasPrefix(d.GrantID, "dg_") && noncePattern.MatchString(strings.TrimPrefix(d.GrantID, "dg_"))
	validScopes := slices.Equal(d.Scopes, []string{"chat.read", "chat.write"}) || slices.Equal(d.Scopes, []string{"chat.read", "chat.write", "space.manage"})
	return validID && validKey && validScopes && d.ExpiresAt > 0 && d.ExpiresAt < 9007199254740991
}

func retentionLeaves(devices []RetainedDevice) ([][]byte, error) {
	if len(devices) == 0 || len(devices) > 256 {
		return nil, ErrProof
	}
	leaves := make([][]byte, len(devices))
	previous := ""
	for i, device := range devices {
		if !validRetainedDevice(device) || device.GrantID <= previous {
			return nil, ErrProof
		}
		previous = device.GrantID
		leaves[i] = retentionLeaf(device)
	}
	return leaves, nil
}

func retentionLevel(nodes [][]byte) [][]byte {
	next := make([][]byte, 0, (len(nodes)+1)/2)
	for i := 0; i < len(nodes); i += 2 {
		right := min(i+1, len(nodes)-1)
		next = append(next, retentionNode(nodes[i], nodes[right]))
	}
	return next
}

// RetentionRoot принимает уже упорядоченный список. Неявная сортировка скрыла бы дубликаты.
func RetentionRoot(devices []RetainedDevice) ([]byte, error) {
	nodes, err := retentionLeaves(devices)
	if err != nil {
		return nil, err
	}
	for len(nodes) > 1 {
		nodes = retentionLevel(nodes)
	}
	return retentionCommitment(nodes[0], len(devices)), nil
}

func ProveRetention(devices []RetainedDevice, index int) (RetentionProof, error) {
	nodes, err := retentionLeaves(devices)
	if err != nil || index < 0 || index >= len(devices) {
		return RetentionProof{}, ErrProof
	}
	proof := RetentionProof{Device: devices[index], Index: index, Count: len(devices)}
	for len(nodes) > 1 {
		sibling := index ^ 1
		if sibling >= len(nodes) {
			sibling = index
		}
		proof.Siblings = append(proof.Siblings, nodes[sibling])
		index /= 2
		nodes = retentionLevel(nodes)
	}
	return proof, nil
}

func VerifyRetention(root []byte, proof RetentionProof) error {
	if len(root) != 32 || !validRetainedDevice(proof.Device) || proof.Count < 1 || proof.Count > 256 || proof.Index < 0 || proof.Index >= proof.Count {
		return ErrProof
	}
	node := retentionLeaf(proof.Device)
	index, count, depth := proof.Index, proof.Count, 0
	for count > 1 {
		if depth >= len(proof.Siblings) {
			return ErrProof
		}
		sibling := proof.Siblings[depth]
		if len(sibling) != 32 || (index^1 >= count && !bytes.Equal(node, sibling)) {
			return ErrProof
		}
		if index%2 == 0 {
			node = retentionNode(node, sibling)
		} else {
			node = retentionNode(sibling, node)
		}
		index, count, depth = index/2, (count+1)/2, depth+1
	}
	if depth != len(proof.Siblings) || !bytes.Equal(retentionCommitment(node, proof.Count), root) {
		return ErrProof
	}
	return nil
}

func retentionCommitment(root []byte, count int) []byte {
	raw := []byte("space/root-retained/root/v1\x00")
	raw = binary.BigEndian.AppendUint32(raw, uint32(count))
	sum := sha256.Sum256(append(raw, root...))
	return sum[:]
}
