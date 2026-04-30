package receipts

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/replay"
)

const indexRelPath = "receipts/index.jsonl"

type Receipt struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id,omitempty"`
	MissionID   string    `json:"mission_id,omitempty"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	Evidence    []string  `json:"evidence,omitempty"`
	ArtifactRef string    `json:"artifact_ref,omitempty"`
	ReplayPath  string    `json:"replay_path,omitempty"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	Signer      string    `json:"signer,omitempty"`
	Signature   string    `json:"signature,omitempty"`
}

type Filter struct {
	TaskID string
	Kind   string
}

func (r *Receipt) Validate() error {
	if r.Kind == "" {
		return fmt.Errorf("receipt: kind is required")
	}
	if r.Summary == "" {
		return fmt.Errorf("receipt: summary is required")
	}
	if r.SHA256 == "" {
		return fmt.Errorf("receipt: sha256 is required")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("receipt: created_at is required")
	}
	return nil
}

func New(kind, summary string, body []byte) Receipt {
	sum := sha256.Sum256(body)
	return Receipt{
		ID:        "receipt-" + uuid.NewString(),
		Kind:      kind,
		Summary:   summary,
		SHA256:    hex.EncodeToString(sum[:]),
		CreatedAt: time.Now().UTC(),
	}
}

func NewReplayReceipt(rec *replay.Recording, replayPath, summary string) (Receipt, error) {
	if rec == nil {
		return Receipt{}, fmt.Errorf("receipt: replay recording is required")
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return Receipt{}, fmt.Errorf("receipt: marshal replay: %w", err)
	}
	receipt := New("replay", summary, body)
	receipt.TaskID = rec.TaskID
	receipt.ReplayPath = replayPath
	receipt.Evidence = []string{rec.ID, rec.Outcome}
	return receipt, nil
}

func Sign(r *Receipt, secret, signer string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signaturePayload(*r)))
	r.Signature = hex.EncodeToString(mac.Sum(nil))
	r.Signer = signer
	return nil
}

func Verify(r Receipt, secret string) bool {
	if r.Signature == "" {
		return false
	}
	want := r
	want.Signature = ""
	got := hmac.New(sha256.New, []byte(secret))
	got.Write([]byte(signaturePayload(want)))
	return hmac.Equal([]byte(strings.ToLower(r.Signature)), []byte(hex.EncodeToString(got.Sum(nil))))
}

func Append(repo string, receipt Receipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	path := r1dir.CanonicalPathFor(repo, indexRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("receipt: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("receipt: open index: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("receipt: marshal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("receipt: write canonical: %w", err)
	}
	if err := r1dir.WriteFileFor(repo, indexRelPath, mustReadAll(path), 0o644); err != nil {
		return fmt.Errorf("receipt: dual-write legacy: %w", err)
	}
	return nil
}

func mustReadAll(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func Load(repo string, filter Filter) ([]Receipt, error) {
	path := r1dir.CanonicalPathFor(repo, indexRelPath)
	data, err := r1dir.ReadFileFor(repo, indexRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("receipt: read %s: %w", path, err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	var out []Receipt
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Receipt
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if filter.TaskID != "" && r.TaskID != filter.TaskID {
			continue
		}
		if filter.Kind != "" && r.Kind != filter.Kind {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func Export(path string, receipt Receipt) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("receipt: marshal export: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("receipt: export %s: %w", path, err)
	}
	return nil
}

func signaturePayload(r Receipt) string {
	return strings.Join([]string{
		r.ID,
		r.TaskID,
		r.MissionID,
		r.Kind,
		r.Summary,
		r.SHA256,
		r.CreatedAt.UTC().Format(time.RFC3339Nano),
		strings.Join(r.Evidence, "|"),
		r.ArtifactRef,
		r.ReplayPath,
	}, "\n")
}
