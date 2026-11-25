package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

const DraftPlanTTL = 15 * time.Minute

const planBindingKey = "planBinding"

type AttachmentRequirement struct {
	ID        string `json:"id"`
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"sizeBytes"`
}

type Requirements struct {
	Attachments []AttachmentRequirement `json:"attachments"`
	Features    []string                `json:"features"`
}

// ResolveRequest is provider-neutral. A capability resolver can implement this
// interface without coupling the chat store to a closed feature enum.
type ResolveRequest struct {
	ThreadID     string
	Target       store.RunTarget
	Requirements Requirements
}

type Resolution struct {
	Blockers                    []store.PlanIssue
	Choices                     []store.PlanChoice
	Notices                     []store.PlanIssue
	StaleEvidence               bool
	CapabilityReport            json.RawMessage
	ConfigFingerprint           string
	ConnectionPolicyFingerprint string
	ThreadPolicyFingerprint     string
}

type Resolver interface {
	ResolveRunRequirements(ctx context.Context, req ResolveRequest) (Resolution, error)
}

type PreflightRequest struct {
	ThreadID     string
	Target       store.RunTarget
	Requirements Requirements
}

type Service struct {
	store    store.Store
	resolver Resolver
	now      func() time.Time
}

func NewService(st store.Store, resolver Resolver) *Service {
	return &Service{store: st, resolver: resolver, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Preflight(ctx context.Context, req PreflightRequest) (store.RunPlan, error) {
	if s == nil || s.store == nil {
		return store.RunPlan{}, errors.New("run preflight store is unavailable")
	}
	req.ThreadID = strings.TrimSpace(req.ThreadID)
	req.Target = NormalizeTarget(req.Target)
	if req.ThreadID == "" {
		return store.RunPlan{}, fmt.Errorf("%w: threadId is required", store.ErrInvalid)
	}
	if req.Target.ConnectionID == "" || req.Target.Model == "" {
		return store.RunPlan{}, fmt.Errorf("%w: target.connectionId and target.model are required", store.ErrInvalid)
	}
	if _, err := s.store.GetThread(ctx, req.ThreadID); err != nil {
		return store.RunPlan{}, err
	}

	requirements := NormalizeRequirements(req.Requirements)
	resolution := Resolution{}
	var err error
	if s.resolver != nil {
		resolution, err = s.resolver.ResolveRunRequirements(ctx, ResolveRequest{
			ThreadID: req.ThreadID, Target: req.Target, Requirements: requirements,
		})
		if err != nil {
			return store.RunPlan{}, err
		}
	}
	if len(resolution.CapabilityReport) == 0 {
		resolution.CapabilityReport, _ = json.Marshal(map[string]any{
			"target":   req.Target,
			"features": map[string]any{},
		})
	}
	if !json.Valid(resolution.CapabilityReport) {
		return store.RunPlan{}, fmt.Errorf("%w: capability report must be valid JSON", store.ErrInvalid)
	}
	if strings.TrimSpace(resolution.ConfigFingerprint) == "" {
		return store.RunPlan{}, fmt.Errorf("%w: resolver routing fingerprint is required", store.ErrInvalid)
	}
	boundReport, err := BindPlanPolicyFingerprints(
		resolution.CapabilityReport, resolution.ConnectionPolicyFingerprint, resolution.ThreadPolicyFingerprint,
	)
	if err != nil {
		return store.RunPlan{}, fmt.Errorf("%w: capability report policy binding is invalid", store.ErrInvalid)
	}
	resolution.CapabilityReport = boundReport

	now := s.now().UTC()
	planID := types.NewUUID()
	resolution.Blockers = normalizeIssues(planID, "blocker", resolution.Blockers)
	resolution.Notices = normalizeIssues(planID, "notice", resolution.Notices)
	resolution.Choices = normalizeChoices(planID, resolution.Choices)
	resultStatus := "clean"
	if len(resolution.Blockers) > 0 {
		resultStatus = "blocked"
	} else if len(resolution.Choices) > 0 {
		resultStatus = "choices_required"
	} else if len(resolution.Notices) > 0 {
		resultStatus = "notices"
	}

	plan := store.RunPlan{
		ID:                planID,
		ThreadID:          req.ThreadID,
		Target:            req.Target,
		ConfigFingerprint: strings.TrimSpace(resolution.ConfigFingerprint),
		RequirementsHash:  RequirementsHash(requirements),
		ResultStatus:      resultStatus,
		Status:            store.RunPlanStatusDraft,
		Blockers:          resolution.Blockers,
		Choices:           resolution.Choices,
		Notices:           resolution.Notices,
		StaleEvidence:     resolution.StaleEvidence,
		CapabilityReport:  append(json.RawMessage(nil), resolution.CapabilityReport...),
		CreatedAt:         now,
		ExpiresAt:         now.Add(DraftPlanTTL),
	}
	return s.store.CreateRunPlan(ctx, plan)
}

// ProviderConnectionPolicyFingerprint binds a preflight result to mutable
// connection capability controls and saved attachment consent without changing
// the evidence-scoped routing fingerprint.
func ProviderConnectionPolicyFingerprint(cfg config.ProviderConfig) string {
	return config.ProviderPlanPolicyFingerprint(cfg)
}

func BindPlanPolicyFingerprints(report json.RawMessage, connectionFingerprint, threadFingerprint string) (json.RawMessage, error) {
	connectionFingerprint = strings.TrimSpace(connectionFingerprint)
	threadFingerprint = strings.TrimSpace(threadFingerprint)
	if connectionFingerprint == "" || threadFingerprint == "" {
		return nil, errors.New("connection and thread policy fingerprints are required")
	}
	var object map[string]any
	if err := json.Unmarshal(report, &object); err != nil || object == nil {
		return nil, errors.New("capability report must be an object")
	}
	object[planBindingKey] = map[string]any{
		"schemaVersion":               1,
		"connectionPolicyFingerprint": connectionFingerprint,
		"threadPolicyFingerprint":     threadFingerprint,
	}
	return json.Marshal(object)
}

// PlanPolicyFingerprints retrieves the immutable policy bindings embedded in
// the persisted report, avoiding a run_plans schema expansion.
func PlanPolicyFingerprints(plan store.RunPlan) (connection, thread string) {
	var object map[string]json.RawMessage
	if json.Unmarshal(plan.CapabilityReport, &object) != nil {
		return "", ""
	}
	var binding struct {
		SchemaVersion int    `json:"schemaVersion"`
		Connection    string `json:"connectionPolicyFingerprint"`
		Thread        string `json:"threadPolicyFingerprint"`
	}
	if json.Unmarshal(object[planBindingKey], &binding) != nil || binding.SchemaVersion != 1 {
		return "", ""
	}
	connection, thread = binding.Connection, binding.Thread
	return strings.TrimSpace(connection), strings.TrimSpace(thread)
}

func NormalizeTarget(in store.RunTarget) store.RunTarget {
	return store.RunTarget{
		ConnectionID: strings.TrimSpace(in.ConnectionID),
		Model:        strings.TrimSpace(in.Model),
	}
}

func NormalizeRequirements(in Requirements) Requirements {
	features := make([]string, 0, len(in.Features))
	seenFeatures := map[string]struct{}{}
	for _, raw := range in.Features {
		feature := strings.TrimSpace(raw)
		if feature == "" {
			continue
		}
		if _, ok := seenFeatures[feature]; ok {
			continue
		}
		seenFeatures[feature] = struct{}{}
		features = append(features, feature)
	}
	sort.Strings(features)

	attachments := make([]AttachmentRequirement, 0, len(in.Attachments))
	seenAttachments := map[string]struct{}{}
	for _, item := range in.Attachments {
		item.ID = strings.TrimSpace(item.ID)
		item.MIME = strings.ToLower(strings.TrimSpace(item.MIME))
		if item.ID == "" {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", item.ID, item.MIME, item.SizeBytes)
		if _, ok := seenAttachments[key]; ok {
			continue
		}
		seenAttachments[key] = struct{}{}
		attachments = append(attachments, item)
	}
	sort.Slice(attachments, func(i, j int) bool {
		if attachments[i].ID != attachments[j].ID {
			return attachments[i].ID < attachments[j].ID
		}
		if attachments[i].MIME != attachments[j].MIME {
			return attachments[i].MIME < attachments[j].MIME
		}
		return attachments[i].SizeBytes < attachments[j].SizeBytes
	})
	return Requirements{Attachments: attachments, Features: features}
}

func RequirementsHash(in Requirements) string {
	normalized := NormalizeRequirements(in)
	b, _ := json.Marshal(normalized)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func RequirementsFromAttachments(images []types.ImageAttachment, files []types.FileAttachment, features []string) Requirements {
	attachments := make([]AttachmentRequirement, 0, len(images)+len(files))
	for _, image := range images {
		size := int64(0)
		if image.SizeBytes != nil {
			size = *image.SizeBytes
		}
		attachments = append(attachments, AttachmentRequirement{ID: image.ArtifactID, MIME: image.MimeType, SizeBytes: size})
	}
	for _, file := range files {
		size := int64(0)
		if file.SizeBytes != nil {
			size = *file.SizeBytes
		}
		mime := file.NormalizedMIME
		if strings.TrimSpace(mime) == "" {
			mime = file.MimeType
		}
		attachments = append(attachments, AttachmentRequirement{ID: file.ArtifactID, MIME: mime, SizeBytes: size})
	}
	return NormalizeRequirements(Requirements{Attachments: attachments, Features: features})
}

func normalizeIssues(planID, bucket string, in []store.PlanIssue) []store.PlanIssue {
	out := make([]store.PlanIssue, 0, len(in))
	seen := map[string]struct{}{}
	for i, issue := range in {
		issue.Code = strings.TrimSpace(issue.Code)
		issue.Text = strings.TrimSpace(issue.Text)
		issue.IssueID = strings.TrimSpace(issue.IssueID)
		if issue.IssueID == "" || hasSeen(seen, issue.IssueID) {
			issue.IssueID = stableIssueID(planID, bucket, i, issue)
		}
		seen[issue.IssueID] = struct{}{}
		out = append(out, issue)
	}
	return out
}

func normalizeChoices(planID string, in []store.PlanChoice) []store.PlanChoice {
	out := make([]store.PlanChoice, 0, len(in))
	seen := map[string]struct{}{}
	for i, choice := range in {
		choice.Code = strings.TrimSpace(choice.Code)
		choice.Text = strings.TrimSpace(choice.Text)
		choice.IssueID = strings.TrimSpace(choice.IssueID)
		if choice.IssueID == "" || hasSeen(seen, choice.IssueID) {
			choice.IssueID = stableIssueID(planID, "choice", i, choice.PlanIssue)
		}
		seen[choice.IssueID] = struct{}{}
		for j := range choice.Options {
			choice.Options[j].ID = strings.TrimSpace(choice.Options[j].ID)
			choice.Options[j].APISurface = strings.TrimSpace(choice.Options[j].APISurface)
		}
		choice.AllowedScopes = normalizeScopes(choice.AllowedScopes)
		out = append(out, choice)
	}
	return out
}

func normalizeScopes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		scope := strings.ToLower(strings.TrimSpace(raw))
		if scope != "run" && scope != "thread" && scope != "connection" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func stableIssueID(planID, bucket string, index int, issue store.PlanIssue) string {
	attachmentID := ""
	if issue.AttachmentID != nil {
		attachmentID = strings.TrimSpace(*issue.AttachmentID)
	}
	feature := ""
	if issue.Feature != nil {
		feature = strings.TrimSpace(*issue.Feature)
	}
	raw := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s", planID, bucket, index, issue.Code, attachmentID, feature)
	sum := sha256.Sum256([]byte(raw))
	return "issue_" + hex.EncodeToString(sum[:12])
}

func hasSeen(seen map[string]struct{}, key string) bool {
	_, ok := seen[key]
	return ok
}
