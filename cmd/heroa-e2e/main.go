// heroa-e2e is a D-008 Phase 5 test program: R1 → Heroa SDK call.
//
// Calls heroa.deploy() against the live control-plane smoketest at
// 34.173.219.177:8090 (port 8090 — the smoketest default), then calls
// heroa.stop() to clean up.
//
// Usage:
//
//	go run ./cmd/heroa-e2e \
//	  -cp http://34.173.219.177:8090 \
//	  -template next-ssr \
//	  -region us-central1 \
//	  -isolation firecracker
//
// Expected output (smoketest mode):
//
//	PHASE5: deploy → instance_id=smk-0001 url=https://smk-0001.heroa.dev state=created
//	PHASE5: billing_events count=2 (instance_create_started + instance_started)
//	PHASE5: stop → OK
//	PHASE5: PASS
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	heroa "heroa.dev/sdk-go"
)

func main() {
	cp := flag.String("cp", "http://34.173.219.177:8090", "control plane base URL")
	template := flag.String("template", "next-ssr", "template to deploy")
	region := flag.String("region", "us-central1", "target region")
	isolation := flag.String("isolation", "firecracker", "isolation mode: firecracker or docker")
	flag.Parse()

	slog.Info("heroa-e2e Phase 5 starting",
		"cp", *cp,
		"template", *template,
		"region", *region,
		"isolation", *isolation,
	)

	client, err := heroa.New(heroa.Config{
		APIKey:         "d008-e2e-test-key",
		BaseURL:        *cp,
		DefaultAppName: "r1-heroa-e2e",
		MaxRetries:     0,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "PHASE5: FAIL — client init: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Phase 5a: deploy
	inst, err := client.Deploy(ctx, heroa.DeployRequest{
		Template:  *template,
		Region:    *region,
		AppName:   "r1-heroa-e2e",
		Isolation: *isolation,
		TTL:       "5m",
		Metadata: map[string]string{
			"source":    "r1-heroa-e2e",
			"test_run":  "d008-e2e-run2",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "PHASE5: FAIL — deploy: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("PHASE5: deploy → instance_id=%s url=%s state=%s region=%s expires_at=%s\n",
		inst.ID, inst.URL, inst.State, inst.Region, inst.ExpiresAt)

	// Phase 5b: query billing events
	evtURL := fmt.Sprintf("%s/v1/billing/events?instance_id=%s", *cp, inst.ID)
	evtResp, err := http.Get(evtURL) //nolint:noctx
	if err != nil {
		fmt.Fprintf(os.Stderr, "PHASE5: WARN — billing events query failed: %v\n", err)
	} else {
		defer evtResp.Body.Close()
		body, _ := io.ReadAll(evtResp.Body)
		var evts struct {
			Events []struct {
				EventType string `json:"event_type"`
				Subject   string `json:"subject"`
			} `json:"events"`
		}
		if err := json.Unmarshal(body, &evts); err == nil {
			fmt.Printf("PHASE5: billing_events count=%d\n", len(evts.Events))
			for _, e := range evts.Events {
				fmt.Printf("  event_type=%s subject=%s\n", e.EventType, e.Subject)
			}
		} else {
			fmt.Printf("PHASE5: billing_events raw=%s\n", string(body))
		}
	}

	// Phase 5c: stop (cleanup)
	stopErr := client.Stop(ctx, "r1-heroa-e2e", inst.ID)
	if stopErr != nil {
		fmt.Fprintf(os.Stderr, "PHASE5: FAIL — stop: %v\n", stopErr)
		os.Exit(1)
	}
	fmt.Printf("PHASE5: stop → OK\n")
	fmt.Printf("PHASE5: PASS\n")
}
