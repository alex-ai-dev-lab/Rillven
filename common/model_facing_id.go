// Adapted from OpenAI Codex, Apache-2.0; see docs/licenses/codex-model-facing-id.txt.
// Source: codex-rs/utils/model-facing-id/src/lib.rs at 662a634e88fcc809d77da393d75572c3d814cb27.
package common

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"

	"github.com/zeebo/blake3"
)

const ModelFacingIDVersion = 1

// ModelFacingID retains the allocation, rather than storing only its display
// form. The versioned generator and next ordinal can resume the same sequence.
type ModelFacingID struct {
	Salt    string `json:"salt"`
	Ordinal uint64 `json:"ordinal"`
	Value   uint64 `json:"value"`
}

func (id ModelFacingID) String() string {
	digits := fmt.Sprint(id.Value)
	width := ((len(digits) + 2) / 3) * 3
	return id.Salt + strings.Repeat("0", width-len(digits)) + digits
}

// ModelFacingIDAt follows Codex's BLAKE3 seed derivation, four-round balanced
// Feistel permutation and cycle walking, including the final u64 decimal pool.
func ModelFacingIDAt(seed uint64, salt string, ordinal uint64) (ModelFacingID, error) {
	if salt == "" {
		return ModelFacingID{}, errors.New("model-facing ID salt is empty")
	}
	input := make([]byte, 8+len(salt))
	binary.LittleEndian.PutUint64(input, seed)
	copy(input[8:], salt)
	hash := blake3.Sum256(input)
	derived := binary.LittleEndian.Uint64(hash[:8])
	return ModelFacingIDFromState(ModelFacingIDVersion, derived, salt, ordinal)
}

func ModelFacingIDFromState(version int, derived uint64, salt string, ordinal uint64) (ModelFacingID, error) {
	if version != ModelFacingIDVersion || salt == "" {
		return ModelFacingID{}, errors.New("unsupported model-facing ID snapshot")
	}
	start, end := uint64(0), uint64(999)
	for ordinal > end {
		start = end + 1
		if start > math.MaxUint64/1000 {
			end = math.MaxUint64
		} else {
			end = start*1000 - 1
		}
	}
	size := end - start + 1
	half := (bits.Len64(size-1) + 1) / 2
	mask := (uint64(1) << half) - 1
	var roundInput [25]byte
	binary.LittleEndian.PutUint64(roundInput[:8], derived)
	binary.LittleEndian.PutUint64(roundInput[8:16], start/1000)
	inputLength := 17 + (half+7)/8
	slot := ordinal - start
	for {
		left, right := slot>>half, slot&mask
		for round := 0; round < 4; round++ {
			roundInput[16] = byte(round)
			binary.LittleEndian.PutUint64(roundInput[17:], right)
			hash := blake3.Sum256(roundInput[:inputLength])
			left, right = right, left^(binary.LittleEndian.Uint64(hash[:8])&mask)
		}
		slot = left<<half | right
		if slot < size {
			return ModelFacingID{Salt: salt, Ordinal: ordinal, Value: start + slot}, nil
		}
	}
}
