package heroa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config configures a Client.
type Config struct {
	APIKey  string
	BaseURL string // default https://api.heroa.dev
	// HTTPClient is an optional override. When nil, a client with a 30s
	// timeout is used.
	HTTPClient *http.Client
	// DefaultAppName is used when DeployRequest.AppName is empty.
	DefaultAppName string
	// MaxRetries is the number of retry attempts beyond the first call for
	// 5xx responses. Default 1.
	MaxRetries int
}

// Client is the Heroa control-plane client.
type Client struct {
	apiKey         string
	baseURL        string
	http           *http.Client
	defaultAppName string
	maxRetries     int
}

// New constructs a Client. Returns an error if APIKey is empty.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("heroa: Config.APIKey is required")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.heroa.dev"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	app := cfg.DefaultAppName
	if app == "" {
		app = "heroa-sdk"
	}
	retries := cfg.MaxRetries
	if retries < 0 {
		retries = 0
	}
	if cfg.MaxRetries == 0 {
		retries = 1
	}
	return &Client{
		apiKey:         cfg.APIKey,
		baseURL:        baseURL,
		http:           client,
		defaultAppName: app,
		maxRetries:     retries,
	}, nil
}

// FileOverlay is a file to write inside the guest at boot via MMDS.
type FileOverlay struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ResourceShape overrides the size-default resources.
type ResourceShape struct {
	CPUs               int `json:"cpus"`
	MemoryMB           int `json:"memory_mb"`
	PersistentVolumeGB int `json:"persistent_volume_gb,omitempty"`
}

// Hooks are lifecycle callbacks invoked on successful / failed deploys.
// Throwing inside a hook does not propagate out of Deploy; it is passed
// to OnError on the next tick.
type Hooks struct {
	OnReady func(*Instance)
	OnStop  func(*Instance)
	OnError func(error)
}

// DeployRequest mirrors scope §5.2.
type DeployRequest struct {
	Template      string
	Region        string
	AppName       string
	Size          string
	TTL           string
	RestartPolicy string
	// Isolation selects the workload isolation mode. Valid values are
	// "firecracker" (Firecracker microVM, default) and "docker" (Docker
	// container on the substrate host). Per D-003, per-load isolation
	// choice is a first-class feature.
	Isolation       string
	Env             map[string]string
	Files           []FileOverlay
	Command         []string
	Resources       *ResourceShape
	CustomHostnames []string
	RegionPolicy    string
	Metadata        map[string]string
	IdempotencyKey  string
	Lifecycle       Hooks
	// EgressPolicy controls outbound traffic from the VM.
	// Valid values: "" / "allow-all" (default), "allowed-domains", "canadian-only".
	EgressPolicy string
	// AllowedDomains lists domain names whose A records are resolved at VM
	// create time and stamped into the host iptables OUTPUT chain as ACCEPT
	// rules. Only meaningful when EgressPolicy="allowed-domains".
	AllowedDomains []string
	// Ingress declares ports to expose from the VM.
	Ingress []IngressPort
}

// IngressPort exposes a VM port publicly or on the internal subnet only.
type IngressPort struct {
	Port       int    `json:"port"`
	Public     bool   `json:"public"`
	ExposePath string `json:"expose_path,omitempty"`
}

// Instance mirrors scope §5.3.
type Instance struct {
	ID        string
	URL       string
	Hostnames []string
	Region    string
	Size      string
	ExpiresAt string
	CreatedAt string
	State     string
	Metadata  map[string]string
}

// on-wire types. Tagged with lowercase JSON names to match the control
// plane's api.CreateMachineRequest exactly.

type createAppRequestWire struct {
	AppName string `json:"app_name"`
	OrgSlug string `json:"org_slug"`
}

type appResponseWire struct {
	ID       string `json:"id"`
	OrgID    string `json:"org_id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

type guestConfigWire struct {
	CPUs     int `json:"cpus"`
	MemoryMB int `json:"memory_mb"`
}

type machineConfigWire struct {
	Image         string            `json:"image"`
	Guest         guestConfigWire   `json:"guest"`
	Env           map[string]string `json:"env"`
	Metadata      map[string]string `json:"metadata"`
	Mounts        []any             `json:"mounts"`
	IsolationMode string            `json:"isolation_mode,omitempty"`
}

type createMachineRequestWire struct {
	Name                 string            `json:"name"`
	Region               string            `json:"region"`
	Config               machineConfigWire `json:"config"`
	TTL                  string            `json:"ttl,omitempty"`
	RestartPolicy        string            `json:"restart_policy,omitempty"`
	Files                []FileOverlay     `json:"files,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	RegionPolicy         string            `json:"region_policy,omitempty"`
	EgressPolicy   string        `json:"egress_policy,omitempty"`
	AllowedDomains []string      `json:"allowed_domains,omitempty"`
	Ingress        []IngressPort `json:"ingress,omitempty"`
}

type machineResponseWire struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	State             string            `json:"state"`
	DesiredState      string            `json:"desired_state"`
	ObservedState     string            `json:"observed_state"`
	Region            string            `json:"region"`
	IPAddress         string            `json:"ip_address,omitempty"`
	GeneratedHostname string            `json:"generated_hostname,omitempty"`
	LastError         string            `json:"last_error,omitempty"`
	Config            machineConfigWire `json:"config"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
	ExpiresAt         string            `json:"expires_at,omitempty"`
	URL               string            `json:"url,omitempty"`
	Hostnames         []string          `json:"hostnames,omitempty"`
}

type errorResponseWire struct {
	Code      ErrorCode         `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// Deploy creates a new instance. Returns *Instance on success, *HeroaError
// on any control-plane failure.
func (c *Client) Deploy(ctx context.Context, req DeployRequest) (*Instance, error) {
	if req.Template == "" {
		return nil, fmt.Errorf("heroa: DeployRequest.Template is required")
	}
	if req.Region == "" {
		return nil, fmt.Errorf("heroa: DeployRequest.Region is required")
	}

	appName := req.AppName
	if appName == "" {
		appName = c.defaultAppName
	}

	inst, err := c.deployInternal(ctx, appName, req)
	if err != nil {
		c.invokeOnError(req.Lifecycle, err)
		return nil, err
	}
	c.invokeOnReady(req.Lifecycle, inst)
	return inst, nil
}

// ── Multi-region instance groups (H12-4) ──

// InstanceGroupRequest is the input for Client.DeployGroup.
// Regions must contain at least 2 valid region identifiers.
type InstanceGroupRequest struct {
	Template     string
	Regions      []string
	AppName      string
	Size         string
	TTL          string
	RegionPolicy string // "strict" (default) | "best-effort"
	RoutingMode  string // "dns-latency" (default) | "explicit-urls" | "sticky-session"
	Env          map[string]string
	Metadata     map[string]string
	Resources    *ResourceShape
}

// InstanceGroupMember is a single placed instance within an InstanceGroup.
type InstanceGroupMember struct {
	Region        string
	FleetMemberID string
	HostID        string
	URL           string
	Status        string
}

// InstanceGroup is a multi-region deployment group returned by DeployGroup.
type InstanceGroup struct {
	ID          string
	AppID       string
	Regions     []string
	RoutingMode string
	AnycastURL  string
	Status      string
	Instances   []InstanceGroupMember
	// Per-region URL map for explicit-urls routing mode.
	URLs map[string]string
}

// on-wire types for instance group API.

type createInstanceGroupWire struct {
	Regions      []string `json:"regions"`
	RoutingMode  string   `json:"routing_mode,omitempty"`
	Template     string   `json:"template"`
	GuestCPUs    int      `json:"guest_cpus,omitempty"`
	GuestMemMB   int      `json:"guest_mem_mb,omitempty"`
	TTL          string   `json:"ttl,omitempty"`
	RegionPolicy string   `json:"region_policy,omitempty"`
}

type instanceGroupResponseWire struct {
	ID          string                    `json:"id"`
	AppID       string                    `json:"app_id"`
	Regions     []string                  `json:"regions"`
	RoutingMode string                    `json:"routing_mode"`
	AnycastURL  string                    `json:"anycast_url,omitempty"`
	Status      string                    `json:"status"`
	Instances   []instanceGroupMemberWire `json:"instances"`
	URLs        map[string]string         `json:"urls,omitempty"`
}

type instanceGroupMemberWire struct {
	Region        string `json:"region"`
	FleetMemberID string `json:"fleet_member_id"`
	HostID        string `json:"host_id"`
	URL           string `json:"url"`
	Status        string `json:"status"`
}

// DeployGroup creates a multi-region instance group with one instance per
// supplied region. At least two regions are required. On strict region_policy
// (the default), all regions must succeed; on best-effort, partial placement
// is accepted. Returns an *InstanceGroup on success, or *HeroaError on failure.
func (c *Client) DeployGroup(ctx context.Context, req InstanceGroupRequest) (*InstanceGroup, error) {
	if req.Template == "" {
		return nil, fmt.Errorf("heroa: InstanceGroupRequest.Template is required")
	}
	if len(req.Regions) < 2 {
		return nil, fmt.Errorf("heroa: InstanceGroupRequest.Regions must contain at least 2 entries")
	}

	appName := req.AppName
	if appName == "" {
		appName = c.defaultAppName
	}
	app, err := c.ensureApp(ctx, appName)
	if err != nil {
		return nil, err
	}

	size := req.Size
	if size == "" {
		size = "small"
	}
	shape, ok := SizeShapes[size]
	if !ok {
		shape = SizeShapes["small"]
	}
	if req.Resources != nil {
		shape = SizeShape{CPUs: req.Resources.CPUs, MemoryMB: req.Resources.MemoryMB}
	}
	routingMode := req.RoutingMode
	if routingMode == "" {
		routingMode = "dns-latency"
	}
	regionPolicy := req.RegionPolicy
	if regionPolicy == "" {
		regionPolicy = "strict"
	}

	wire := createInstanceGroupWire{
		Regions:      req.Regions,
		RoutingMode:  routingMode,
		Template:     req.Template,
		GuestCPUs:    shape.CPUs,
		GuestMemMB:   shape.MemoryMB,
		TTL:          req.TTL,
		RegionPolicy: regionPolicy,
	}

	path := "/v1/apps/" + app.ID + "/instance-groups"
	resp, err := c.do(ctx, http.MethodPost, path, wire, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, buildError(resp)
	}
	var wireResp instanceGroupResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &HeroaError{Code: ErrCodeInternal, Status: resp.StatusCode,
			Message: "decode instance group response: " + err.Error()}
	}
	return wireToInstanceGroup(&wireResp), nil
}

// DestroyGroup destroys a multi-region instance group.
func (c *Client) DestroyGroup(ctx context.Context, appName, groupID string) error {
	if appName == "" {
		return fmt.Errorf("heroa: appName is required")
	}
	if groupID == "" {
		return fmt.Errorf("heroa: groupID is required")
	}
	path := "/v1/apps/" + appName + "/instance-groups/" + groupID
	resp, err := c.do(ctx, http.MethodDelete, path, struct{}{}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return buildError(resp)
}

// GetGroup returns the current state of an instance group.
func (c *Client) GetGroup(ctx context.Context, appName, groupID string) (*InstanceGroup, error) {
	if appName == "" {
		return nil, fmt.Errorf("heroa: appName is required")
	}
	if groupID == "" {
		return nil, fmt.Errorf("heroa: groupID is required")
	}
	path := "/v1/apps/" + appName + "/instance-groups/" + groupID
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, buildError(resp)
	}
	var wrapper struct {
		InstanceGroup instanceGroupResponseWire `json:"instance_group"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, &HeroaError{Code: ErrCodeInternal, Status: resp.StatusCode,
			Message: "decode instance group response: " + err.Error()}
	}
	return wireToInstanceGroup(&wrapper.InstanceGroup), nil
}

func wireToInstanceGroup(w *instanceGroupResponseWire) *InstanceGroup {
	members := make([]InstanceGroupMember, 0, len(w.Instances))
	for _, m := range w.Instances {
		members = append(members, InstanceGroupMember{
			Region:        m.Region,
			FleetMemberID: m.FleetMemberID,
			HostID:        m.HostID,
			URL:           m.URL,
			Status:        m.Status,
		})
	}
	urls := w.URLs
	if urls == nil {
		urls = map[string]string{}
	}
	return &InstanceGroup{
		ID:          w.ID,
		AppID:       w.AppID,
		Regions:     w.Regions,
		RoutingMode: w.RoutingMode,
		AnycastURL:  w.AnycastURL,
		Status:      w.Status,
		Instances:   members,
		URLs:        urls,
	}
}

// ── End multi-region instance groups ──

// Stop destroys an instance by issuing DELETE /v1/apps/{app}/machines/{id}.
// The instance is identified by its ID as returned by Deploy. Returns nil on
// success (204 or 200), or a *HeroaError on any control-plane failure.
//
// appName is resolved to an app ID via ensureApp (idempotent POST /v1/apps)
// because the CP routes /v1/apps/{app}/... by ID, not by name.
func (c *Client) Stop(ctx context.Context, appName, instanceID string) error {
	if appName == "" {
		return fmt.Errorf("heroa: appName is required")
	}
	if instanceID == "" {
		return fmt.Errorf("heroa: instanceID is required")
	}
	app, err := c.ensureApp(ctx, appName)
	if err != nil {
		return err
	}
	path := "/v1/apps/" + app.ID + "/machines/" + instanceID
	resp, err := c.do(ctx, http.MethodDelete, path, struct{}{}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return buildError(resp)
}

func (c *Client) deployInternal(ctx context.Context, appName string, req DeployRequest) (*Instance, error) {
	// ensureApp returns the app record with its server-assigned ID. Subsequent
	// calls use app.ID (not appName) because the CP routes /v1/apps/{app}/...
	// by ID. Using the name returns 404 on a non-smoketest control-plane.
	app, err := c.ensureApp(ctx, appName)
	if err != nil {
		return nil, err
	}
	wire := requestToWire(appName, req)
	idemKey := req.IdempotencyKey
	if idemKey == "" {
		k, err := canonicalSha256(wire)
		if err != nil {
			return nil, &HeroaError{Code: ErrCodeInternal, Message: err.Error()}
		}
		idemKey = k
	}
	mresp, err := c.createMachine(ctx, app.ID, wire, idemKey)
	if err != nil {
		return nil, err
	}
	inst := wireToInstance(mresp)
	return &inst, nil
}

func (c *Client) ensureApp(ctx context.Context, name string) (*appResponseWire, error) {
	body := createAppRequestWire{AppName: name, OrgSlug: ""}
	resp, err := c.do(ctx, http.MethodPost, "/v1/apps", body, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, buildError(resp)
	}
	var out appResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &HeroaError{Code: ErrCodeInternal, Status: resp.StatusCode,
			Message: "decode app response: " + err.Error()}
	}
	return &out, nil
}

func (c *Client) createMachine(
	ctx context.Context,
	appName string,
	wire createMachineRequestWire,
	idemKey string,
) (*machineResponseWire, error) {
	path := "/v1/apps/" + appName + "/machines"
	resp, err := c.do(ctx, http.MethodPost, path, wire, map[string]string{
		"Idempotency-Key": idemKey,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, buildError(resp)
	}
	var out machineResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &HeroaError{Code: ErrCodeInternal, Status: resp.StatusCode,
			Message: "decode machine response: " + err.Error()}
	}
	return &out, nil
}

func (c *Client) do(
	ctx context.Context,
	method, path string,
	body any,
	extra map[string]string,
) (*http.Response, error) {
	url := c.baseURL + path
	payload, err := canonicalJSON(body)
	if err != nil {
		return nil, &HeroaError{Code: ErrCodeInternal, Message: "canonicalize: " + err.Error()}
	}
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
		if rerr != nil {
			return nil, &HeroaError{Code: ErrCodeInternal, Message: rerr.Error()}
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", UserAgent())
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, derr := c.http.Do(req)
		if derr != nil {
			lastErr = derr
			if attempt < c.maxRetries {
				backoff(ctx, attempt)
				continue
			}
			return nil, &HeroaError{Code: ErrCodeInternal, Message: "transport: " + derr.Error()}
		}
		if resp.StatusCode >= 500 && attempt < c.maxRetries {
			// Drain + close so we can retry the socket.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			backoff(ctx, attempt)
			continue
		}
		return resp, nil
	}
	return nil, &HeroaError{Code: ErrCodeInternal, Message: "retry exhausted: " + lastErr.Error()}
}

// UserAgent returns the SDK user-agent string used on every outbound
// HTTP request. Exported so the contract test can assert against it.
func UserAgent() string { return "heroa-sdk-go/" + SDKVersion }

func backoff(ctx context.Context, attempt int) {
	d := time.Duration(100*pow4(attempt)) * time.Millisecond
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func pow4(n int) int {
	p := 1
	for i := 0; i < n; i++ {
		p *= 4
	}
	return p
}

func buildError(resp *http.Response) error {
	bodyBytes, _ := io.ReadAll(resp.Body)
	var body errorResponseWire
	if err := json.Unmarshal(bodyBytes, &body); err != nil || body.Code == "" {
		return &HeroaError{
			Code:    ErrCodeInternal,
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("unrecognized error body (status %d)", resp.StatusCode),
		}
	}
	return &HeroaError{
		Code:      body.Code,
		Status:    resp.StatusCode,
		Message:   body.Message,
		RequestID: body.RequestID,
		Details:   body.Details,
	}
}

func requestToWire(appName string, req DeployRequest) createMachineRequestWire {
	size := req.Size
	if size == "" {
		size = "small"
	}
	shape, ok := SizeShapes[size]
	if !ok {
		shape = SizeShapes["small"]
	}
	if req.Resources != nil {
		shape = SizeShape{CPUs: req.Resources.CPUs, MemoryMB: req.Resources.MemoryMB}
	}
	env := req.Env
	if env == nil {
		env = map[string]string{}
	}
	meta := req.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	isolation := req.Isolation
	if isolation == "" {
		isolation = "firecracker"
	}
	return createMachineRequestWire{
		Name:   appName,
		Region: req.Region,
		Config: machineConfigWire{
			Image:         req.Template,
			Guest:         guestConfigWire{CPUs: shape.CPUs, MemoryMB: shape.MemoryMB},
			Env:           env,
			Metadata:      meta,
			Mounts:        []any{},
			IsolationMode: isolation,
		},
		TTL:            req.TTL,
		RestartPolicy:  req.RestartPolicy,
		Files:          req.Files,
		Metadata:       meta,
		RegionPolicy:   req.RegionPolicy,
		EgressPolicy:   req.EgressPolicy,
		AllowedDomains: req.AllowedDomains,
		Ingress:        req.Ingress,
	}
}

func wireToInstance(m *machineResponseWire) Instance {
	url := m.URL
	if url == "" && m.GeneratedHostname != "" {
		url = "https://" + m.GeneratedHostname
	}
	hostnames := m.Hostnames
	if len(hostnames) == 0 && m.GeneratedHostname != "" {
		hostnames = []string{m.GeneratedHostname}
	}
	meta := m.Config.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	state := m.State
	if state == "" {
		state = m.ObservedState
	}
	return Instance{
		ID:        m.ID,
		URL:       url,
		Hostnames: hostnames,
		Region:    m.Region,
		Size:      classifySize(m.Config.Guest.CPUs, m.Config.Guest.MemoryMB),
		ExpiresAt: m.ExpiresAt,
		CreatedAt: m.CreatedAt,
		State:     state,
		Metadata:  meta,
	}
}

func classifySize(cpus, memMB int) string {
	switch {
	case cpus <= 1 && memMB <= 256:
		return "nano"
	case cpus <= 1 && memMB <= 512:
		return "small"
	case cpus <= 2 && memMB <= 2048:
		return "medium"
	case cpus <= 4 && memMB <= 8192:
		return "large"
	default:
		return "xl"
	}
}

func (c *Client) invokeOnReady(h Hooks, inst *Instance) {
	if h.OnReady == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("heroa: OnReady hook panicked: %v", r)
			if h.OnError != nil {
				safeInvokeOnError(h, err)
			}
		}
	}()
	h.OnReady(inst)
}

func (c *Client) invokeOnError(h Hooks, err error) {
	if h.OnError == nil {
		return
	}
	safeInvokeOnError(h, err)
}

func safeInvokeOnError(h Hooks, err error) {
	defer func() {
		// OnError itself panicked. Swallow — hooks never escape Deploy.
		_ = recover()
	}()
	h.OnError(err)
}

// Exec / ExecStream are intentionally absent from the SDK.
//
// The control-plane /v1/apps/{app}/machines/{id}/exec route returns
// 501 Not Implemented (see cmd/control-plane execMachine) and
// /exec/stream is not registered at all. The real exec path lands in
// H4-4 (guest-side agent + gRPC) — when that ships, the SDK methods
// land in the same commit as the server route. Until then keeping
// the SDK methods would advertise a path the server never honors.
