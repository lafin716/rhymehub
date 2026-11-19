package domain

import (
	"encoding/json"
	"time"
)

// Enumerations stored as TEXT with CHECK constraints in the schema.
type (
	RequestSource string
	RequestType   string
	ReleaseStatus string
	NotifyStatus  string
	AttachKind    string
)

const (
	SourceDashboard RequestSource = "dashboard"
	SourceSDK       RequestSource = "sdk"
	SourceCLI       RequestSource = "cli"
	SourceAPI       RequestSource = "api"

	RequestBug         RequestType = "bug"
	RequestFeature     RequestType = "feature"
	RequestImprovement RequestType = "improvement"
	RequestRefactor    RequestType = "refactor"
	RequestPerformance RequestType = "performance"
	RequestSecurity    RequestType = "security"
	RequestDocs        RequestType = "docs"
	RequestRelease     RequestType = "release"

	ReleaseBuilding   ReleaseStatus = "building"
	ReleasePublished  ReleaseStatus = "published"
	ReleaseFailed     ReleaseStatus = "failed"
	ReleaseRolledBack ReleaseStatus = "rolled_back"

	NotifyPending NotifyStatus = "pending"
	NotifySent    NotifyStatus = "sent"
	NotifyFailed  NotifyStatus = "failed"
	NotifySkipped NotifyStatus = "skipped"

	AttachScreenshot AttachKind = "screenshot"
	AttachLog        AttachKind = "log"
	AttachFile       AttachKind = "file"
)

// Valid reports whether the source is one of the four documented sources.
func (s RequestSource) Valid() bool {
	switch s {
	case SourceDashboard, SourceSDK, SourceCLI, SourceAPI:
		return true
	}
	return false
}

// Valid reports whether the type is one of the eight documented request types.
func (t RequestType) Valid() bool {
	switch t {
	case RequestBug, RequestFeature, RequestImprovement, RequestRefactor, RequestPerformance,
		RequestSecurity, RequestDocs, RequestRelease:
		return true
	}
	return false
}

// User is a dashboard/CLI account.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Session is a browser session. Only the SHA-256 of the cookie token is stored.
type Session struct {
	TokenHash string    `json:"-"`
	UserID    string    `json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// APIToken is a CLI/automation token ("rht_..."). Only its hash is stored.
type APIToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"userId"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

// Project is a managed repository plus its policies.
type Project struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	RepoPath             string    `json:"repoPath"`
	GitURL               string    `json:"gitUrl"`
	DefaultBranch        string    `json:"defaultBranch"`
	Type                 string    `json:"type"`
	Stack                string    `json:"stack"`
	BuildCmd             string    `json:"buildCmd"`
	TestCmd              string    `json:"testCmd"`
	ReleaseStrategy      string    `json:"releaseStrategy"`
	ReleaseCmd           string    `json:"releaseCmd"`
	DefaultAgent         string    `json:"defaultAgent"`
	DefaultModel         string    `json:"defaultModel"`
	MaxConcurrent        int       `json:"maxConcurrent"`
	TimeoutMinutes       int       `json:"timeoutMinutes"`
	AutoTaskFromRequests bool      `json:"autoTaskFromRequests"`
	AutoApprove          bool      `json:"autoApprove"`
	AutoRelease          bool      `json:"autoRelease"`
	AllowedTools         []string  `json:"allowedTools"`
	SDKStatus            string    `json:"sdkStatus"`
	InstalledVersion     string    `json:"installedVersion"`
	LatestVersion        string    `json:"latestVersion"`
	IsSelf               bool      `json:"isSelf"`
	Archived             bool      `json:"archived"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// ProjectKey is an SDK reporter key. Only the hash is stored; KeyPrefix is shown in listings.
type ProjectKey struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	Name           string     `json:"name"`
	KeyHash        string     `json:"-"`
	KeyPrefix      string     `json:"keyPrefix"`
	AllowedOrigins []string   `json:"allowedOrigins"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastUsedAt     *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
}

// Diagnostic is one redacted diagnostic item attached to a request.
type Diagnostic struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Content      string `json:"content,omitempty"`
	AttachmentID string `json:"attachmentId,omitempty"`
}

// Request is a Development Inbox item.
type Request struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"projectId"`
	Source         RequestSource `json:"source"`
	Type           RequestType   `json:"type"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Priority       int           `json:"priority"`
	Status         RequestStatus `json:"status"`
	InstallationID string        `json:"installationId"`
	AppVersion     string        `json:"appVersion"`
	Platform       string        `json:"platform"`
	ReporterUserID string        `json:"reporterUserId"`
	ReporterKeyID  string        `json:"reporterKeyId"`
	Diagnostics    []Diagnostic  `json:"diagnostics"`
	Redactions     int           `json:"redactions"`
	TaskID         string        `json:"taskId"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

// Attachment is an uploaded, redacted file stored outside worktrees.
type Attachment struct {
	ID        string     `json:"id"`
	RequestID string     `json:"requestId"`
	Kind      AttachKind `json:"kind"`
	Filename  string     `json:"filename"`
	Mime      string     `json:"mime"`
	Size      int64      `json:"size"`
	Path      string     `json:"-"`
	Redacted  bool       `json:"redacted"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Task is one agent work item.
type Task struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"projectId"`
	RequestID       string          `json:"requestId"`
	Title           string          `json:"title"`
	Prompt          string          `json:"prompt"`
	Agent           string          `json:"agent"`
	Model           string          `json:"model"`
	State           TaskState       `json:"state"`
	Priority        int             `json:"priority"`
	Branch          string          `json:"branch"`
	WorktreePath    string          `json:"worktreePath"`
	BaseCommit      string          `json:"baseCommit"`
	HeadCommit      string          `json:"headCommit"`
	SessionID       string          `json:"sessionId"`
	NextInput       string          `json:"nextInput"`
	AllowedTools    []string        `json:"allowedTools"`
	PendingQuestion string          `json:"pendingQuestion"`
	PendingApproval json.RawMessage `json:"pendingApproval"`
	WorkerPID       int             `json:"workerPid"`
	HeartbeatAt     *time.Time      `json:"heartbeatAt,omitempty"`
	Attempt         int             `json:"attempt"`
	MaxAttempts     int             `json:"maxAttempts"`
	Turns           int             `json:"turns"`
	TestStatus      string          `json:"testStatus"`
	TestOutput      string          `json:"testOutput"`
	DiffStat        string          `json:"diffStat"`
	ResultSummary   string          `json:"resultSummary"`
	Error           string          `json:"error"`
	FailureReason   string          `json:"failureReason"`
	CostUSD         float64         `json:"costUsd"`
	ReleaseID       string          `json:"releaseId"`
	CreatedBy       string          `json:"createdBy"`
	ApprovedBy      string          `json:"approvedBy"`
	Version         int64           `json:"version"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
}

// TaskEvent is one entry of the per-task event log. ID is the global, monotonic id used for SSE replay.
type TaskEvent struct {
	ID        int64           `json:"id"`
	TaskID    string          `json:"taskId"`
	Seq       int64           `json:"seq"`
	Type      EventType       `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	IdemKey   string          `json:"-"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Artifact describes one release artifact.
type Artifact struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

// Release is a published (or attempted) release of a project.
type Release struct {
	ID         string        `json:"id"`
	ProjectID  string        `json:"projectId"`
	Version    string        `json:"version"`
	Tag        string        `json:"tag"`
	Strategy   string        `json:"strategy"`
	TaskIDs    []string      `json:"taskIds"`
	Notes      string        `json:"notes"`
	Status     ReleaseStatus `json:"status"`
	Artifacts  []Artifact    `json:"artifacts"`
	Log        string        `json:"log"`
	CreatedBy  string        `json:"createdBy"`
	ApprovedBy string        `json:"approvedBy"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
}

// AuditEntry is one append-only audit record.
type AuditEntry struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Actor      string          `json:"actor"`
	ActorRole  string          `json:"actorRole"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Detail     json.RawMessage `json:"detail"`
	IP         string          `json:"ip"`
}

// PushSubscription is a Web Push endpoint for a user.
type PushSubscription struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	Endpoint  string    `json:"endpoint"`
	P256DH    string    `json:"-"`
	Auth      string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

// Notification is an outbound notification with dedupe key EventKey.
type Notification struct {
	ID            string       `json:"id"`
	EventKey      string       `json:"-"`
	Kind          string       `json:"kind"`
	Title         string       `json:"title"`
	Body          string       `json:"body"`
	URL           string       `json:"url"`
	ProjectID     string       `json:"projectId"`
	TaskID        string       `json:"taskId"`
	Status        NotifyStatus `json:"status"`
	Attempts      int          `json:"attempts"`
	LastError     string       `json:"-"`
	ReadAt        *time.Time   `json:"readAt,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	NextAttemptAt time.Time    `json:"-"`
}
