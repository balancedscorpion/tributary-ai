package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ai-analyst-local/internal/atomicfile"
)

type ProviderSelection struct {
	ProviderID string  `json:"providerId"`
	Model      string  `json:"model"`
	ProfileID  *string `json:"profileId,omitempty"`
}

type ProviderAuth struct {
	Type   string  `json:"type"`             // "none" | "api_key"
	Header string  `json:"header"`           // e.g. Authorization, x-api-key
	Scheme *string `json:"scheme,omitempty"` // e.g. Bearer
}

type HeaderKV struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret,omitempty"`
}

type CompatibleProfileModel struct {
	ID          string  `json:"id"`
	Label       *string `json:"label,omitempty"`
	Description *string `json:"description,omitempty"`
	Recommended *bool   `json:"recommended,omitempty"`
}

type ProviderWebSearchConfig struct {
	Enabled           *bool    `json:"enabled,omitempty"`
	ToolType          *string  `json:"toolType,omitempty"`
	MaxUses           *int     `json:"maxUses,omitempty"`
	SearchContextSize *string  `json:"searchContextSize,omitempty"`
	AllowedDomains    []string `json:"allowedDomains,omitempty"`
	BlockedDomains    []string `json:"blockedDomains,omitempty"`
	AllowedCallers    []string `json:"allowedCallers,omitempty"`
}

type ProviderCodeExecutionSkill struct {
	Type    string  `json:"type"`
	SkillID string  `json:"skillId"`
	Version *string `json:"version,omitempty"`
}

type ProviderCodeExecutionConfig struct {
	Enabled       *bool                        `json:"enabled,omitempty"`
	ToolType      *string                      `json:"toolType,omitempty"`
	ContainerMode *string                      `json:"containerMode,omitempty"`
	ContainerID   *string                      `json:"containerId,omitempty"`
	MemoryLimit   *string                      `json:"memoryLimit,omitempty"`
	Skills        []ProviderCodeExecutionSkill `json:"skills,omitempty"`
}

type CompatibleProfile struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	ApiType     string `json:"apiType"` // "chat_completions" | "responses"
	BaseURL     string `json:"baseUrl"`

	Auth      ProviderAuth `json:"auth"`
	ApiKey    *string      `json:"apiKey,omitempty"`
	Extra     []HeaderKV   `json:"extraHeaders"`
	Query     any          `json:"queryParams,omitempty"`
	Overrides any          `json:"payloadOverrides,omitempty"`
	TimeoutMs any          `json:"timeoutMs,omitempty"`

	ReasoningEffort *string `json:"reasoningEffort,omitempty"`

	ModelInPath        any                          `json:"modelInPath,omitempty"`
	ModelInBody        any                          `json:"modelInBody,omitempty"`
	EndpointSufx       *string                      `json:"endpointSuffix,omitempty"`
	FileInputMode      string                       `json:"fileInputMode,omitempty"`
	IncludeStreamUsage *bool                        `json:"includeStreamUsage,omitempty"`
	UsageSemantics     *string                      `json:"usageSemantics,omitempty"`
	WebSearch          *ProviderWebSearchConfig     `json:"webSearch,omitempty"`
	CodeExecution      *ProviderCodeExecutionConfig `json:"codeExecution,omitempty"`

	Models []CompatibleProfileModel `json:"models,omitempty"`
}

type OpenAIConfig struct {
	ApiKey            string                       `json:"apiKey"`
	BaseURL           *string                      `json:"baseUrl,omitempty"`
	CompactionEnabled *bool                        `json:"compactionEnabled,omitempty"`
	CompactionTrigger *int                         `json:"compactionTriggerTokens,omitempty"`
	WebSearch         *ProviderWebSearchConfig     `json:"webSearch,omitempty"`
	CodeExecution     *ProviderCodeExecutionConfig `json:"codeExecution,omitempty"`
}

type AnthropicConfig struct {
	ApiKey                string                       `json:"apiKey"`
	BaseURL               *string                      `json:"baseUrl,omitempty"`
	AnthropicVersion      *string                      `json:"anthropicVersion,omitempty"`
	CompactionEnabled     *bool                        `json:"compactionEnabled,omitempty"`
	CompactionInstruction *string                      `json:"compactionInstruction,omitempty"`
	CompactionTrigger     *int                         `json:"compactionTriggerTokens,omitempty"`
	WebSearch             *ProviderWebSearchConfig     `json:"webSearch,omitempty"`
	CodeExecution         *ProviderCodeExecutionConfig `json:"codeExecution,omitempty"`
}

type GeminiConfig struct {
	ApiKey  string  `json:"apiKey"`
	BaseURL *string `json:"baseUrl,omitempty"`
}

type BedrockConfig struct {
	Region                string                       `json:"region"`
	BaseURL               *string                      `json:"baseUrl,omitempty"`
	AnthropicVersion      *string                      `json:"anthropicVersion,omitempty"`
	CompactionEnabled     *bool                        `json:"compactionEnabled,omitempty"`
	CompactionInstruction *string                      `json:"compactionInstruction,omitempty"`
	CompactionTrigger     *int                         `json:"compactionTriggerTokens,omitempty"`
	Extra                 []HeaderKV                   `json:"extraHeaders"`
	ApiKey                string                       `json:"apiKey,omitempty"`
	AccessKeyID           string                       `json:"accessKeyId,omitempty"`
	SecretAccessKey       string                       `json:"secretAccessKey,omitempty"`
	SessionToken          *string                      `json:"sessionToken,omitempty"`
	WebSearch             *ProviderWebSearchConfig     `json:"webSearch,omitempty"`
	CodeExecution         *ProviderCodeExecutionConfig `json:"codeExecution,omitempty"`
}

type ProviderFeatureFlags struct {
	OpenAI     bool `json:"openai"`
	Anthropic  bool `json:"anthropic"`
	Gemini     bool `json:"gemini"`
	Bedrock    bool `json:"bedrock"`
	Compatible bool `json:"compatible"`
}

type ChartEngineFeatureFlags struct {
	Echarts    bool `json:"echarts"`
	Highcharts bool `json:"highcharts"`
}

type CapabilityUXFeatureFlags struct {
	Report         bool `json:"report"`
	EditorV2       bool `json:"editorV2"`
	Controls       bool `json:"controls"`
	Preflight      bool `json:"preflight"`
	ComposerTarget bool `json:"composerTarget"`
}

type FeatureFlags struct {
	Activation                *bool                    `json:"activation"`
	Providers                 ProviderFeatureFlags     `json:"providers"`
	ProviderTemplates         bool                     `json:"providerTemplates"`
	CloudCredentialsAWS       bool                     `json:"cloudCredentialsAws"`
	CloudCredentialsTrino     bool                     `json:"cloudCredentialsTrino"`
	CloudCredentialsSnowflake bool                     `json:"cloudCredentialsSnowflake"`
	ChartDimensionFilters     bool                     `json:"chartDimensionFilters"`
	ChartProgramPipeline      *bool                    `json:"chartProgramPipeline,omitempty"`
	ChartEngines              *ChartEngineFeatureFlags `json:"chartEngines,omitempty"`
	CapabilityUX              CapabilityUXFeatureFlags `json:"capabilityUx"`
}

type DatasourceConnectionState struct {
	Enabled                 *bool   `json:"enabled,omitempty"`
	LastCheckedAt           *int64  `json:"lastCheckedAt,omitempty"`
	LastCheckOk             *bool   `json:"lastCheckOk,omitempty"`
	LastCheckMessage        *string `json:"lastCheckMessage,omitempty"`
	LastVerifiedFingerprint *string `json:"lastVerifiedFingerprint,omitempty"`
	LastProjectID           *string `json:"lastProjectId,omitempty"`
	LastWarehouseID         *string `json:"lastWarehouseId,omitempty"`
	LastHost                *string `json:"lastHost,omitempty"`
	AutomaticScanBytes      *int64  `json:"automaticScanBytes,omitempty"`
	ApprovedScanBytes       *int64  `json:"approvedScanBytes,omitempty"`
}

type DatasourceConnections struct {
	BigQuery   *DatasourceConnectionState `json:"bigquery,omitempty"`
	Databricks *DatasourceConnectionState `json:"databricks,omitempty"`
	Trino      *DatasourceConnectionState `json:"trino,omitempty"`
	Snowflake  *DatasourceConnectionState `json:"snowflake,omitempty"`
}

type ConnectionCatalogID string

const (
	ConnectionCatalogThreadSearch    ConnectionCatalogID = "thread-search"
	ConnectionCatalogImageGeneration ConnectionCatalogID = "image-generation"
	ConnectionCatalogWebSearch       ConnectionCatalogID = "web-search"
	ConnectionCatalogPythonSandbox   ConnectionCatalogID = "python-sandbox"
	ConnectionCatalogLocalComputer   ConnectionCatalogID = "local-computer"
	ConnectionCatalogBrowser         ConnectionCatalogID = "browser"
	ConnectionCatalogGitHub          ConnectionCatalogID = "github"
	ConnectionCatalogSlack           ConnectionCatalogID = "slack"
	ConnectionCatalogAtlassian       ConnectionCatalogID = "atlassian"
	ConnectionCatalogSharePoint      ConnectionCatalogID = "sharepoint"
	ConnectionCatalogArxiv           ConnectionCatalogID = "arxiv"
	ConnectionCatalogBigQuery        ConnectionCatalogID = "bigquery"
	ConnectionCatalogDatabricks      ConnectionCatalogID = "databricks"
	ConnectionCatalogTrino           ConnectionCatalogID = "trino"
	ConnectionCatalogSnowflake       ConnectionCatalogID = "snowflake"
)

type ConnectionsSettings struct {
	VisibleIDs []ConnectionCatalogID `json:"visibleIds,omitempty"`
}

type ThreadSearchSettings struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type LocalComputerAccessMode string

const (
	LocalComputerAccessModeWholeComputer   LocalComputerAccessMode = "whole-computer"
	LocalComputerAccessModeSelectedFolders LocalComputerAccessMode = "selected-folders"
)

type LocalComputerSettings struct {
	Enabled          *bool                    `json:"enabled,omitempty"`
	AccessMode       *LocalComputerAccessMode `json:"accessMode,omitempty"`
	SelectedFolders  []string                 `json:"selectedFolders,omitempty"`
	LastCheckedAt    *int64                   `json:"lastCheckedAt,omitempty"`
	LastCheckOk      *bool                    `json:"lastCheckOk,omitempty"`
	LastCheckMessage *string                  `json:"lastCheckMessage,omitempty"`
}

type AtlassianProductSettings struct {
	Enabled          *bool   `json:"enabled,omitempty"`
	ShowBrowser      *bool   `json:"showBrowser,omitempty"`
	LastCheckedAt    *int64  `json:"lastCheckedAt,omitempty"`
	LastCheckOk      *bool   `json:"lastCheckOk,omitempty"`
	LastCheckMessage *string `json:"lastCheckMessage,omitempty"`
}

type AtlassianSettings struct {
	JiraBaseURL             *string                   `json:"jiraBaseUrl,omitempty"`
	ConfluenceBaseURL       *string                   `json:"confluenceBaseUrl,omitempty"`
	DefaultJiraProjectKey   *string                   `json:"defaultJiraProjectKey,omitempty"`
	AllowedProjects         []string                  `json:"allowedProjects,omitempty"`
	AllowedAuthDomains      []string                  `json:"allowedAuthDomains,omitempty"`
	BrowserProvider         *string                   `json:"browserProvider,omitempty"` // "bundled"
	BrowserChannel          *string                   `json:"browserChannel,omitempty"`
	BrowserExecutablePath   *string                   `json:"browserExecutablePath,omitempty"`
	BrowserProfileDir       *string                   `json:"browserProfileDir,omitempty"`
	LastVerifiedFingerprint *string                   `json:"lastVerifiedFingerprint,omitempty"`
	Jira                    *AtlassianProductSettings `json:"jira,omitempty"`
	Confluence              *AtlassianProductSettings `json:"confluence,omitempty"`
}

type BrowserControlSettings struct {
	Enabled           *bool   `json:"enabled,omitempty"`
	ShowBrowser       *bool   `json:"showBrowser,omitempty"`
	BrowserProfileDir *string `json:"browserProfileDir,omitempty"`
}

type SharePointSettings struct {
	SiteURL                 *string `json:"siteUrl,omitempty"`
	Enabled                 *bool   `json:"enabled,omitempty"`
	ShowBrowser             *bool   `json:"showBrowser,omitempty"`
	BrowserProfileDir       *string `json:"browserProfileDir,omitempty"`
	AuthMode                *string `json:"authMode,omitempty"` // currently "browser"; reserved for Graph/MSAL later.
	LastCheckedAt           *int64  `json:"lastCheckedAt,omitempty"`
	LastCheckOk             *bool   `json:"lastCheckOk,omitempty"`
	LastCheckMessage        *string `json:"lastCheckMessage,omitempty"`
	LastVerifiedFingerprint *string `json:"lastVerifiedFingerprint,omitempty"`
}

type GitHubSettings struct {
	Enabled                 *bool    `json:"enabled,omitempty"`
	ReadTools               *bool    `json:"readTools,omitempty"`
	AllowedRepositories     []string `json:"allowedRepositories,omitempty"`
	LastCheckedAt           *int64   `json:"lastCheckedAt,omitempty"`
	LastCheckOk             *bool    `json:"lastCheckOk,omitempty"`
	LastCheckMessage        *string  `json:"lastCheckMessage,omitempty"`
	LastVerifiedFingerprint *string  `json:"lastVerifiedFingerprint,omitempty"`
}

type SlackSettings struct {
	Enabled                 *bool    `json:"enabled,omitempty"`
	ReadTools               *bool    `json:"readTools,omitempty"`
	WriteTools              *bool    `json:"writeTools,omitempty"`
	WriteIdentity           *string  `json:"writeIdentity,omitempty"`
	AllowedTeamIDs          []string `json:"allowedTeamIds,omitempty"`
	AllowedChannelIDs       []string `json:"allowedChannelIds,omitempty"`
	LastCheckedAt           *int64   `json:"lastCheckedAt,omitempty"`
	LastCheckOk             *bool    `json:"lastCheckOk,omitempty"`
	LastCheckMessage        *string  `json:"lastCheckMessage,omitempty"`
	LastVerifiedFingerprint *string  `json:"lastVerifiedFingerprint,omitempty"`
}

type ArxivSettings struct {
	Enabled          *bool   `json:"enabled,omitempty"`
	ContactEmail     *string `json:"contactEmail,omitempty"`
	LastCheckedAt    *int64  `json:"lastCheckedAt,omitempty"`
	LastCheckOk      *bool   `json:"lastCheckOk,omitempty"`
	LastCheckMessage *string `json:"lastCheckMessage,omitempty"`
}

type ImageGenerationDefaults struct {
	Size    *string `json:"size,omitempty"`
	Quality *string `json:"quality,omitempty"`
	Format  *string `json:"format,omitempty"`
}

type ImageGenerationSettings struct {
	Enabled         *bool                    `json:"enabled,omitempty"`
	ProviderID      *string                  `json:"providerId,omitempty"`
	Model           *string                  `json:"model,omitempty"`
	Defaults        *ImageGenerationDefaults `json:"defaults,omitempty"`
	MaxImagesPerRun *int                     `json:"maxImagesPerRun,omitempty"`
}

type ExternalAPIKey struct {
	ID                string   `json:"id"`
	Name              string   `json:"name,omitempty"`
	Prefix            string   `json:"prefix,omitempty"`
	Hash              string   `json:"hash"`
	Scopes            []string `json:"scopes,omitempty"`
	AllowedThreadIDs  []string `json:"allowedThreadIds,omitempty"`
	AllowedProjectIDs []string `json:"allowedProjectIds,omitempty"`
	CreatedAt         int64    `json:"createdAt,omitempty"`
	LastUsedAt        *int64   `json:"lastUsedAt,omitempty"`
	RevokedAt         *int64   `json:"revokedAt,omitempty"`
}

type ExternalAPISettings struct {
	APIKeys []ExternalAPIKey `json:"apiKeys,omitempty"`
}

type AWSCloudCredential struct {
	ID                 string  `json:"id"`
	DisplayName        string  `json:"displayName"`
	Region             string  `json:"region"`
	AuthMode           string  `json:"authMode"`
	AccessKeyID        *string `json:"accessKeyId,omitempty"`
	SecretAccessKeySet *bool   `json:"secretAccessKeySet,omitempty"`
	SessionTokenSet    *bool   `json:"sessionTokenSet,omitempty"`
	ProfileName        *string `json:"profileName,omitempty"`
	RoleArn            *string `json:"roleArn,omitempty"`
	LastTestedAt       *int64  `json:"lastTestedAt,omitempty"`
	LastTestStatus     *string `json:"lastTestStatus,omitempty"`
	LastTestMessage    *string `json:"lastTestMessage,omitempty"`
}

type GCPCloudCredential struct {
	ID              string  `json:"id"`
	DisplayName     string  `json:"displayName"`
	ProjectID       *string `json:"projectId,omitempty"`
	ADCMode         string  `json:"adcMode"`
	CredentialsPath *string `json:"credentialsPath,omitempty"`
	LastTestedAt    *int64  `json:"lastTestedAt,omitempty"`
	LastTestStatus  *string `json:"lastTestStatus,omitempty"`
	LastTestMessage *string `json:"lastTestMessage,omitempty"`
}

type CloudCredentials struct {
	AWS []AWSCloudCredential `json:"aws"`
	GCP []GCPCloudCredential `json:"gcp"`
}

type ProviderCloudCredentialRefs struct {
	AWSCredentialID *string `json:"awsCredentialId,omitempty"`
	GCPCredentialID *string `json:"gcpCredentialId,omitempty"`
}

type ProviderAttachmentPolicy struct {
	ID         string `json:"id"`
	Class      string `json:"class"`
	Action     string `json:"action"`
	APISurface string `json:"apiSurface"`
	CreatedAt  int64  `json:"createdAt"`
}

type ProviderConfig struct {
	ID                    string                       `json:"id"`
	DisplayName           string                       `json:"displayName"`
	ProviderID            string                       `json:"providerId"`
	Model                 string                       `json:"model"`
	ApiType               *string                      `json:"apiType,omitempty"`
	APISurface            *string                      `json:"apiSurface,omitempty"`
	BaseURL               *string                      `json:"baseUrl,omitempty"`
	AuthType              *string                      `json:"authType,omitempty"`
	AuthHeader            *string                      `json:"authHeader,omitempty"`
	AuthScheme            *string                      `json:"authScheme,omitempty"`
	CloudCredentialRefs   *ProviderCloudCredentialRefs `json:"cloudCredentialRefs,omitempty"`
	Region                *string                      `json:"region,omitempty"`
	AnthropicVersion      *string                      `json:"anthropicVersion,omitempty"`
	CompactionEnabled     *bool                        `json:"compactionEnabled,omitempty"`
	CompactionInstruction *string                      `json:"compactionInstruction,omitempty"`
	CompactionTrigger     *int                         `json:"compactionTriggerTokens,omitempty"`
	EndpointSuffix        *string                      `json:"endpointSuffix,omitempty"`
	TimeoutMs             *int                         `json:"timeoutMs,omitempty"`
	ReasoningEffort       *string                      `json:"reasoningEffort,omitempty"`
	ModelInPath           *bool                        `json:"modelInPath,omitempty"`
	ModelInBody           *bool                        `json:"modelInBody,omitempty"`
	FileInputMode         *string                      `json:"fileInputMode,omitempty"`
	IncludeStreamUsage    *bool                        `json:"includeStreamUsage,omitempty"`
	UsageSemantics        *string                      `json:"usageSemantics,omitempty"`
	ExtraHeaders          []HeaderKV                   `json:"extraHeaders,omitempty"`
	QueryParams           map[string]string            `json:"queryParams,omitempty"`
	PayloadOverrides      map[string]any               `json:"payloadOverrides,omitempty"`
	CapabilityControls    map[string]string            `json:"capabilityControls,omitempty"`
	AttachmentPolicies    []ProviderAttachmentPolicy   `json:"attachmentPolicies,omitempty"`
	ConfigFingerprint     *string                      `json:"configFingerprint,omitempty"`
	WebSearch             *ProviderWebSearchConfig     `json:"webSearch,omitempty"`
	CodeExecution         *ProviderCodeExecutionConfig `json:"codeExecution,omitempty"`
	IsDefault             *bool                        `json:"isDefault,omitempty"`
	CreatedAt             *int64                       `json:"createdAt,omitempty"`
	UpdatedAt             *int64                       `json:"updatedAt,omitempty"`
	LastTestedAt          *int64                       `json:"lastTestedAt,omitempty"`
	LastTestStatus        *string                      `json:"lastTestStatus,omitempty"`
	LastTestMessage       *string                      `json:"lastTestMessage,omitempty"`
	LastTestEndpoint      *string                      `json:"lastTestEndpoint,omitempty"`
}

type ProviderTemplate struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Provider map[string]any `json:"provider"`
}

// DatabaseQueryPolicy is the local, durable policy for long-horizon database
// work. Serialized names include their units so persisted settings remain
// unambiguous if runtime duration types change later.
type DatabaseQueryPolicy struct {
	MaxExecutingGlobal               int   `json:"maxExecutingGlobal"`
	MaxExecutingPerThread            int   `json:"maxExecutingPerThread"`
	MaxExecutingPerConnection        int   `json:"maxExecutingPerConnection"`
	InlineWaitMilliseconds           int64 `json:"inlineWaitMilliseconds"`
	MaxPollIntervalMilliseconds      int64 `json:"maxPollIntervalMilliseconds"`
	MonitorErrorBackoffMilliseconds  int64 `json:"monitorErrorBackoffMilliseconds"`
	SoftQueueTimeoutMilliseconds     int64 `json:"softQueueTimeoutMilliseconds"`
	SoftExecutionTimeoutMilliseconds int64 `json:"softExecutionTimeoutMilliseconds"`
	HardExecutionTimeoutMilliseconds int64 `json:"hardExecutionTimeoutMilliseconds"`
	MaxResultRows                    int   `json:"maxResultRows"`
	MaxMaterializedBytes             int64 `json:"maxMaterializedBytes"`
	BigQueryMaximumBytesBilled       int64 `json:"bigQueryMaximumBytesBilled"`
	MaxAutoContinuations             int   `json:"maxAutoContinuations"`
}

type AppSettings struct {
	Provider                   ProviderSelection        `json:"provider"`
	OpenAI                     *OpenAIConfig            `json:"openai,omitempty"`
	Anthropic                  *AnthropicConfig         `json:"anthropic,omitempty"`
	Gemini                     *GeminiConfig            `json:"gemini,omitempty"`
	Bedrock                    *BedrockConfig           `json:"bedrock,omitempty"`
	FeatureFlags               *FeatureFlags            `json:"featureFlags,omitempty"`
	CloudCredentials           *CloudCredentials        `json:"cloudCredentials,omitempty"`
	ProviderConfigs            []ProviderConfig         `json:"providerConfigs,omitempty"`
	ProviderTemplates          []ProviderTemplate       `json:"providerTemplates,omitempty"`
	ActiveProviderConfigID     *string                  `json:"activeProviderConfigId,omitempty"`
	CompatibleProfiles         []CompatibleProfile      `json:"compatibleProfiles"`
	SystemPrompt               string                   `json:"systemPrompt"`
	GoogleProjectID            *string                  `json:"googleProjectId,omitempty"`
	GoogleAccessToken          *string                  `json:"googleAccessToken,omitempty"` // legacy; never persisted as plain text
	GoogleCredentialsPath      *string                  `json:"googleCredentialsPath,omitempty"`
	DatabricksHost             *string                  `json:"databricksHost,omitempty"`
	DatabricksWarehouseID      *string                  `json:"databricksWarehouseId,omitempty"`
	DatabricksOAuthClientID    *string                  `json:"databricksOAuthClientId,omitempty"`
	GitHubOAuthClientID        *string                  `json:"githubOAuthClientId,omitempty"`
	SlackOAuthClientID         *string                  `json:"slackOAuthClientId,omitempty"`
	TrinoHost                  *string                  `json:"trinoHost,omitempty"`
	TrinoCatalog               *string                  `json:"trinoCatalog,omitempty"`
	TrinoSchema                *string                  `json:"trinoSchema,omitempty"`
	TrinoUser                  *string                  `json:"trinoUser,omitempty"`
	TrinoSource                *string                  `json:"trinoSource,omitempty"`
	SnowflakeAccountIdentifier *string                  `json:"snowflakeAccountIdentifier,omitempty"`
	SnowflakeWarehouse         *string                  `json:"snowflakeWarehouse,omitempty"`
	SnowflakeDatabase          *string                  `json:"snowflakeDatabase,omitempty"`
	SnowflakeSchema            *string                  `json:"snowflakeSchema,omitempty"`
	SnowflakeRole              *string                  `json:"snowflakeRole,omitempty"`
	SnowflakeUser              *string                  `json:"snowflakeUser,omitempty"`
	DataSources                *DatasourceConnections   `json:"dataSources,omitempty"`
	Connections                *ConnectionsSettings     `json:"connections,omitempty"`
	ThreadSearch               *ThreadSearchSettings    `json:"threadSearch,omitempty"`
	LocalComputer              *LocalComputerSettings   `json:"localComputer,omitempty"`
	Atlassian                  *AtlassianSettings       `json:"atlassian,omitempty"`
	BrowserControl             *BrowserControlSettings  `json:"browserControl,omitempty"`
	SharePoint                 *SharePointSettings      `json:"sharepoint,omitempty"`
	GitHub                     *GitHubSettings          `json:"github,omitempty"`
	Slack                      *SlackSettings           `json:"slack,omitempty"`
	Arxiv                      *ArxivSettings           `json:"arxiv,omitempty"`
	ImageGeneration            *ImageGenerationSettings `json:"imageGeneration,omitempty"`
	ExternalAPI                *ExternalAPISettings     `json:"externalApi,omitempty"`
	MaxToolIterations          *int                     `json:"maxToolIterations,omitempty"`
	DatabaseQueryRowLimit      *int                     `json:"databaseQueryRowLimit,omitempty"`
	DatabaseQueryPolicy        *DatabaseQueryPolicy     `json:"databaseQueryPolicy,omitempty"`
	Debug                      *bool                    `json:"debug,omitempty"`
	AdditionalRawFields        map[string]any           `json:"-"`
}

func (s AppSettings) Clone() AppSettings {
	out := s
	if s.OpenAI != nil {
		cfg := *s.OpenAI
		cfg.BaseURL = cloneStringPtr(s.OpenAI.BaseURL)
		cfg.CompactionEnabled = cloneBoolPtr(s.OpenAI.CompactionEnabled)
		cfg.CompactionTrigger = cloneIntPtr(s.OpenAI.CompactionTrigger)
		cfg.WebSearch = cloneProviderWebSearchConfig(s.OpenAI.WebSearch)
		cfg.CodeExecution = cloneProviderCodeExecutionConfig(s.OpenAI.CodeExecution)
		out.OpenAI = &cfg
	}
	if s.Anthropic != nil {
		cfg := *s.Anthropic
		cfg.BaseURL = cloneStringPtr(s.Anthropic.BaseURL)
		cfg.AnthropicVersion = cloneStringPtr(s.Anthropic.AnthropicVersion)
		cfg.CompactionEnabled = cloneBoolPtr(s.Anthropic.CompactionEnabled)
		cfg.CompactionInstruction = cloneStringPtr(s.Anthropic.CompactionInstruction)
		cfg.CompactionTrigger = cloneIntPtr(s.Anthropic.CompactionTrigger)
		cfg.WebSearch = cloneProviderWebSearchConfig(s.Anthropic.WebSearch)
		cfg.CodeExecution = cloneProviderCodeExecutionConfig(s.Anthropic.CodeExecution)
		out.Anthropic = &cfg
	}
	if s.Gemini != nil {
		cfg := *s.Gemini
		cfg.BaseURL = cloneStringPtr(s.Gemini.BaseURL)
		out.Gemini = &cfg
	}
	if s.Bedrock != nil {
		cfg := *s.Bedrock
		cfg.BaseURL = cloneStringPtr(s.Bedrock.BaseURL)
		cfg.AnthropicVersion = cloneStringPtr(s.Bedrock.AnthropicVersion)
		cfg.CompactionEnabled = cloneBoolPtr(s.Bedrock.CompactionEnabled)
		cfg.CompactionInstruction = cloneStringPtr(s.Bedrock.CompactionInstruction)
		cfg.CompactionTrigger = cloneIntPtr(s.Bedrock.CompactionTrigger)
		cfg.SessionToken = cloneStringPtr(s.Bedrock.SessionToken)
		cfg.WebSearch = cloneProviderWebSearchConfig(s.Bedrock.WebSearch)
		cfg.CodeExecution = cloneProviderCodeExecutionConfig(s.Bedrock.CodeExecution)
		if s.Bedrock.Extra != nil {
			cfg.Extra = append([]HeaderKV(nil), s.Bedrock.Extra...)
		}
		out.Bedrock = &cfg
	}
	if s.CompatibleProfiles != nil {
		out.CompatibleProfiles = make([]CompatibleProfile, 0, len(s.CompatibleProfiles))
		for _, profile := range s.CompatibleProfiles {
			cloned := profile
			cloned.ApiKey = cloneStringPtr(profile.ApiKey)
			cloned.TimeoutMs = cloneAny(profile.TimeoutMs)
			cloned.ModelInPath = cloneAny(profile.ModelInPath)
			cloned.ModelInBody = cloneAny(profile.ModelInBody)
			cloned.EndpointSufx = cloneStringPtr(profile.EndpointSufx)
			cloned.IncludeStreamUsage = cloneBoolPtr(profile.IncludeStreamUsage)
			cloned.UsageSemantics = cloneStringPtr(profile.UsageSemantics)
			cloned.ReasoningEffort = cloneStringPtr(profile.ReasoningEffort)
			cloned.WebSearch = cloneProviderWebSearchConfig(profile.WebSearch)
			cloned.CodeExecution = cloneProviderCodeExecutionConfig(profile.CodeExecution)
			if profile.Extra != nil {
				cloned.Extra = append([]HeaderKV(nil), profile.Extra...)
			}
			if profile.Models != nil {
				cloned.Models = append([]CompatibleProfileModel(nil), profile.Models...)
			}
			cloned.Query = cloneAny(profile.Query)
			cloned.Overrides = cloneAny(profile.Overrides)
			out.CompatibleProfiles = append(out.CompatibleProfiles, cloned)
		}
	}
	if s.ProviderConfigs != nil {
		out.ProviderConfigs = make([]ProviderConfig, 0, len(s.ProviderConfigs))
		for _, cfg := range s.ProviderConfigs {
			cloned := cfg
			cloned.ApiType = cloneStringPtr(cfg.ApiType)
			cloned.APISurface = cloneStringPtr(cfg.APISurface)
			cloned.BaseURL = cloneStringPtr(cfg.BaseURL)
			cloned.AuthType = cloneStringPtr(cfg.AuthType)
			cloned.AuthHeader = cloneStringPtr(cfg.AuthHeader)
			cloned.AuthScheme = cloneStringPtr(cfg.AuthScheme)
			cloned.Region = cloneStringPtr(cfg.Region)
			cloned.AnthropicVersion = cloneStringPtr(cfg.AnthropicVersion)
			cloned.CompactionEnabled = cloneBoolPtr(cfg.CompactionEnabled)
			cloned.CompactionInstruction = cloneStringPtr(cfg.CompactionInstruction)
			cloned.CompactionTrigger = cloneIntPtr(cfg.CompactionTrigger)
			cloned.EndpointSuffix = cloneStringPtr(cfg.EndpointSuffix)
			cloned.TimeoutMs = cloneIntPtr(cfg.TimeoutMs)
			cloned.ReasoningEffort = cloneStringPtr(cfg.ReasoningEffort)
			cloned.ModelInPath = cloneBoolPtr(cfg.ModelInPath)
			cloned.ModelInBody = cloneBoolPtr(cfg.ModelInBody)
			cloned.FileInputMode = cloneStringPtr(cfg.FileInputMode)
			cloned.IncludeStreamUsage = cloneBoolPtr(cfg.IncludeStreamUsage)
			cloned.UsageSemantics = cloneStringPtr(cfg.UsageSemantics)
			cloned.ConfigFingerprint = cloneStringPtr(cfg.ConfigFingerprint)
			cloned.WebSearch = cloneProviderWebSearchConfig(cfg.WebSearch)
			cloned.CodeExecution = cloneProviderCodeExecutionConfig(cfg.CodeExecution)
			cloned.IsDefault = cloneBoolPtr(cfg.IsDefault)
			cloned.CreatedAt = cloneInt64Ptr(cfg.CreatedAt)
			cloned.UpdatedAt = cloneInt64Ptr(cfg.UpdatedAt)
			cloned.LastTestedAt = cloneInt64Ptr(cfg.LastTestedAt)
			cloned.LastTestStatus = cloneStringPtr(cfg.LastTestStatus)
			cloned.LastTestMessage = cloneStringPtr(cfg.LastTestMessage)
			cloned.LastTestEndpoint = cloneStringPtr(cfg.LastTestEndpoint)
			if cfg.CloudCredentialRefs != nil {
				refs := *cfg.CloudCredentialRefs
				refs.AWSCredentialID = cloneStringPtr(cfg.CloudCredentialRefs.AWSCredentialID)
				refs.GCPCredentialID = cloneStringPtr(cfg.CloudCredentialRefs.GCPCredentialID)
				cloned.CloudCredentialRefs = &refs
			}
			if cfg.ExtraHeaders != nil {
				cloned.ExtraHeaders = append([]HeaderKV(nil), cfg.ExtraHeaders...)
			}
			if cfg.QueryParams != nil {
				cloned.QueryParams = make(map[string]string, len(cfg.QueryParams))
				for key, value := range cfg.QueryParams {
					cloned.QueryParams[key] = value
				}
			}
			cloned.PayloadOverrides = cloneMapAny(cfg.PayloadOverrides)
			if cfg.CapabilityControls != nil {
				cloned.CapabilityControls = make(map[string]string, len(cfg.CapabilityControls))
				for key, value := range cfg.CapabilityControls {
					cloned.CapabilityControls[key] = value
				}
			}
			if cfg.AttachmentPolicies != nil {
				cloned.AttachmentPolicies = append([]ProviderAttachmentPolicy(nil), cfg.AttachmentPolicies...)
			}
			out.ProviderConfigs = append(out.ProviderConfigs, cloned)
		}
	}
	if s.ProviderTemplates != nil {
		out.ProviderTemplates = make([]ProviderTemplate, 0, len(s.ProviderTemplates))
		for _, tpl := range s.ProviderTemplates {
			cloned := tpl
			cloned.Provider = cloneMapAny(tpl.Provider)
			out.ProviderTemplates = append(out.ProviderTemplates, cloned)
		}
	}
	if s.FeatureFlags != nil {
		ff := *s.FeatureFlags
		ff.Activation = cloneBoolPtr(s.FeatureFlags.Activation)
		ff.ChartProgramPipeline = cloneBoolPtr(s.FeatureFlags.ChartProgramPipeline)
		ff.ChartEngines = cloneChartEngineFeatureFlagsPtr(s.FeatureFlags.ChartEngines)
		out.FeatureFlags = &ff
	}
	if s.CloudCredentials != nil {
		cc := *s.CloudCredentials
		cc.AWS = append([]AWSCloudCredential(nil), s.CloudCredentials.AWS...)
		cc.GCP = append([]GCPCloudCredential(nil), s.CloudCredentials.GCP...)
		out.CloudCredentials = &cc
	}
	if s.DataSources != nil {
		ds := DatasourceConnections{
			BigQuery:   cloneDatasourceConnectionState(s.DataSources.BigQuery),
			Databricks: cloneDatasourceConnectionState(s.DataSources.Databricks),
			Trino:      cloneDatasourceConnectionState(s.DataSources.Trino),
			Snowflake:  cloneDatasourceConnectionState(s.DataSources.Snowflake),
		}
		out.DataSources = &ds
	}
	if s.Connections != nil {
		out.Connections = cloneConnectionsSettings(s.Connections)
	}
	if s.ThreadSearch != nil {
		out.ThreadSearch = cloneThreadSearchSettings(s.ThreadSearch)
	}
	if s.LocalComputer != nil {
		out.LocalComputer = cloneLocalComputerSettings(s.LocalComputer)
	}
	if s.Atlassian != nil {
		atl := *s.Atlassian
		atl.JiraBaseURL = cloneStringPtr(s.Atlassian.JiraBaseURL)
		atl.ConfluenceBaseURL = cloneStringPtr(s.Atlassian.ConfluenceBaseURL)
		atl.DefaultJiraProjectKey = cloneStringPtr(s.Atlassian.DefaultJiraProjectKey)
		atl.BrowserProvider = cloneStringPtr(s.Atlassian.BrowserProvider)
		atl.BrowserChannel = cloneStringPtr(s.Atlassian.BrowserChannel)
		atl.BrowserExecutablePath = cloneStringPtr(s.Atlassian.BrowserExecutablePath)
		atl.BrowserProfileDir = cloneStringPtr(s.Atlassian.BrowserProfileDir)
		atl.LastVerifiedFingerprint = cloneStringPtr(s.Atlassian.LastVerifiedFingerprint)
		atl.AllowedProjects = cloneStringSlice(s.Atlassian.AllowedProjects)
		atl.AllowedAuthDomains = cloneStringSlice(s.Atlassian.AllowedAuthDomains)
		atl.Jira = cloneAtlassianProductSettings(s.Atlassian.Jira)
		atl.Confluence = cloneAtlassianProductSettings(s.Atlassian.Confluence)
		out.Atlassian = &atl
	}
	if s.BrowserControl != nil {
		browserControl := *s.BrowserControl
		browserControl.Enabled = cloneBoolPtr(s.BrowserControl.Enabled)
		browserControl.ShowBrowser = cloneBoolPtr(s.BrowserControl.ShowBrowser)
		browserControl.BrowserProfileDir = cloneStringPtr(s.BrowserControl.BrowserProfileDir)
		out.BrowserControl = &browserControl
	}
	if s.SharePoint != nil {
		out.SharePoint = cloneSharePointSettings(s.SharePoint)
	}
	if s.GitHub != nil {
		out.GitHub = cloneGitHubSettings(s.GitHub)
	}
	if s.Slack != nil {
		out.Slack = cloneSlackSettings(s.Slack)
	}
	if s.Arxiv != nil {
		out.Arxiv = cloneArxivSettings(s.Arxiv)
	}
	if s.ImageGeneration != nil {
		out.ImageGeneration = cloneImageGenerationSettings(s.ImageGeneration)
	}
	if s.ExternalAPI != nil {
		out.ExternalAPI = cloneExternalAPISettings(s.ExternalAPI)
	}
	if s.DatabaseQueryPolicy != nil {
		policy := *s.DatabaseQueryPolicy
		out.DatabaseQueryPolicy = &policy
	}
	return out
}

func strptr(s string) *string { return &s }
func normalizeCompactionInstructionText(value string) string {
	v := strings.TrimSpace(value)
	if v == "" || v == legacyBedrockCompactionInstruction {
		return defaultBedrockCompactionInstruction
	}
	return v
}
func normalizeCompactionTriggerTokens(value *int) *int {
	v := defaultCompactionTriggerTokens
	if value != nil {
		v = *value
	}
	if v < minCompactionTriggerTokens {
		v = minCompactionTriggerTokens
	}
	return &v
}

func normalizeOpenAICompactionTriggerTokens(value *int) *int {
	v := defaultOpenAICompactionTriggerTokens
	if value != nil {
		v = *value
	}
	if v < minCompactionTriggerTokens {
		v = minCompactionTriggerTokens
	}
	return &v
}
func canonicalWebSearchConfig(providerID string, enabled bool) *ProviderWebSearchConfig {
	maxUses := 5
	switch strings.TrimSpace(providerID) {
	case "openai", "compatible":
		toolType := "web_search"
		return &ProviderWebSearchConfig{
			Enabled:  &enabled,
			ToolType: &toolType,
		}
	case "anthropic":
		toolType := "web_search_20260209"
		return &ProviderWebSearchConfig{
			Enabled:        &enabled,
			ToolType:       &toolType,
			MaxUses:        &maxUses,
			AllowedCallers: []string{"direct"},
		}
	default:
		return nil
	}
}

func defaultWebSearchConfig(providerID string) *ProviderWebSearchConfig {
	return canonicalWebSearchConfig(providerID, true)
}
func cloneStringPtr(src *string) *string {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
func cloneBoolPtr(src *bool) *bool {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
func cloneChartEngineFeatureFlagsPtr(src *ChartEngineFeatureFlags) *ChartEngineFeatureFlags {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
func cloneInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
func cloneIntPtr(src *int) *int {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
func cloneStringSlice(src []string) []string {
	if src == nil {
		return nil
	}
	return append([]string(nil), src...)
}
func cloneAny(src any) any {
	if src == nil {
		return nil
	}
	b, err := json.Marshal(src)
	if err != nil {
		return src
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return src
	}
	return out
}
func cloneMapAny(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	b, err := json.Marshal(src)
	if err != nil {
		out := make(map[string]any, len(src))
		for key, value := range src {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}
func cloneProviderWebSearchConfig(src *ProviderWebSearchConfig) *ProviderWebSearchConfig {
	if src == nil {
		return nil
	}
	out := *src
	out.Enabled = cloneBoolPtr(src.Enabled)
	out.ToolType = cloneStringPtr(src.ToolType)
	out.MaxUses = cloneIntPtr(src.MaxUses)
	out.SearchContextSize = cloneStringPtr(src.SearchContextSize)
	out.AllowedDomains = cloneStringSlice(src.AllowedDomains)
	out.BlockedDomains = cloneStringSlice(src.BlockedDomains)
	out.AllowedCallers = cloneStringSlice(src.AllowedCallers)
	return &out
}
func cloneProviderCodeExecutionConfig(src *ProviderCodeExecutionConfig) *ProviderCodeExecutionConfig {
	if src == nil {
		return nil
	}
	out := *src
	out.Enabled = cloneBoolPtr(src.Enabled)
	out.ToolType = cloneStringPtr(src.ToolType)
	out.ContainerMode = cloneStringPtr(src.ContainerMode)
	out.ContainerID = cloneStringPtr(src.ContainerID)
	out.MemoryLimit = cloneStringPtr(src.MemoryLimit)
	if src.Skills != nil {
		out.Skills = make([]ProviderCodeExecutionSkill, 0, len(src.Skills))
		for _, skill := range src.Skills {
			cloned := skill
			cloned.Version = cloneStringPtr(skill.Version)
			out.Skills = append(out.Skills, cloned)
		}
	}
	return &out
}
func cloneDatasourceConnectionState(src *DatasourceConnectionState) *DatasourceConnectionState {
	if src == nil {
		return nil
	}
	return &DatasourceConnectionState{
		Enabled:                 cloneBoolPtr(src.Enabled),
		LastCheckedAt:           cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:             cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage:        cloneStringPtr(src.LastCheckMessage),
		LastVerifiedFingerprint: cloneStringPtr(src.LastVerifiedFingerprint),
		LastProjectID:           cloneStringPtr(src.LastProjectID),
		LastWarehouseID:         cloneStringPtr(src.LastWarehouseID),
		LastHost:                cloneStringPtr(src.LastHost),
		AutomaticScanBytes:      cloneInt64Ptr(src.AutomaticScanBytes),
		ApprovedScanBytes:       cloneInt64Ptr(src.ApprovedScanBytes),
	}
}

func cloneConnectionsSettings(src *ConnectionsSettings) *ConnectionsSettings {
	if src == nil {
		return nil
	}
	return &ConnectionsSettings{
		VisibleIDs: append([]ConnectionCatalogID(nil), src.VisibleIDs...),
	}
}

func cloneThreadSearchSettings(src *ThreadSearchSettings) *ThreadSearchSettings {
	if src == nil {
		return nil
	}
	return &ThreadSearchSettings{
		Enabled: cloneBoolPtr(src.Enabled),
	}
}

func cloneLocalComputerSettings(src *LocalComputerSettings) *LocalComputerSettings {
	if src == nil {
		return nil
	}
	return &LocalComputerSettings{
		Enabled:          cloneBoolPtr(src.Enabled),
		AccessMode:       cloneLocalComputerAccessModePtr(src.AccessMode),
		SelectedFolders:  cloneStringSlice(src.SelectedFolders),
		LastCheckedAt:    cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:      cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage: cloneStringPtr(src.LastCheckMessage),
	}
}

func cloneLocalComputerAccessModePtr(src *LocalComputerAccessMode) *LocalComputerAccessMode {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func cloneAtlassianProductSettings(src *AtlassianProductSettings) *AtlassianProductSettings {
	if src == nil {
		return nil
	}
	return &AtlassianProductSettings{
		Enabled:          cloneBoolPtr(src.Enabled),
		ShowBrowser:      cloneBoolPtr(src.ShowBrowser),
		LastCheckedAt:    cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:      cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage: cloneStringPtr(src.LastCheckMessage),
	}
}

func cloneGitHubSettings(src *GitHubSettings) *GitHubSettings {
	if src == nil {
		return nil
	}
	return &GitHubSettings{
		Enabled:                 cloneBoolPtr(src.Enabled),
		ReadTools:               cloneBoolPtr(src.ReadTools),
		AllowedRepositories:     cloneStringSlice(src.AllowedRepositories),
		LastCheckedAt:           cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:             cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage:        cloneStringPtr(src.LastCheckMessage),
		LastVerifiedFingerprint: cloneStringPtr(src.LastVerifiedFingerprint),
	}
}

func cloneSlackSettings(src *SlackSettings) *SlackSettings {
	if src == nil {
		return nil
	}
	return &SlackSettings{
		Enabled:                 cloneBoolPtr(src.Enabled),
		ReadTools:               cloneBoolPtr(src.ReadTools),
		WriteTools:              cloneBoolPtr(src.WriteTools),
		WriteIdentity:           cloneStringPtr(src.WriteIdentity),
		AllowedTeamIDs:          cloneStringSlice(src.AllowedTeamIDs),
		AllowedChannelIDs:       cloneStringSlice(src.AllowedChannelIDs),
		LastCheckedAt:           cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:             cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage:        cloneStringPtr(src.LastCheckMessage),
		LastVerifiedFingerprint: cloneStringPtr(src.LastVerifiedFingerprint),
	}
}

func cloneArxivSettings(src *ArxivSettings) *ArxivSettings {
	if src == nil {
		return nil
	}
	return &ArxivSettings{
		Enabled:          cloneBoolPtr(src.Enabled),
		ContactEmail:     cloneStringPtr(src.ContactEmail),
		LastCheckedAt:    cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:      cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage: cloneStringPtr(src.LastCheckMessage),
	}
}

func cloneImageGenerationSettings(src *ImageGenerationSettings) *ImageGenerationSettings {
	if src == nil {
		return nil
	}
	out := &ImageGenerationSettings{
		Enabled:         cloneBoolPtr(src.Enabled),
		ProviderID:      cloneStringPtr(src.ProviderID),
		Model:           cloneStringPtr(src.Model),
		MaxImagesPerRun: cloneIntPtr(src.MaxImagesPerRun),
	}
	if src.Defaults != nil {
		out.Defaults = &ImageGenerationDefaults{
			Size:    cloneStringPtr(src.Defaults.Size),
			Quality: cloneStringPtr(src.Defaults.Quality),
			Format:  cloneStringPtr(src.Defaults.Format),
		}
	}
	return out
}

func cloneSharePointSettings(src *SharePointSettings) *SharePointSettings {
	if src == nil {
		return nil
	}
	return &SharePointSettings{
		SiteURL:                 cloneStringPtr(src.SiteURL),
		Enabled:                 cloneBoolPtr(src.Enabled),
		ShowBrowser:             cloneBoolPtr(src.ShowBrowser),
		BrowserProfileDir:       cloneStringPtr(src.BrowserProfileDir),
		AuthMode:                cloneStringPtr(src.AuthMode),
		LastCheckedAt:           cloneInt64Ptr(src.LastCheckedAt),
		LastCheckOk:             cloneBoolPtr(src.LastCheckOk),
		LastCheckMessage:        cloneStringPtr(src.LastCheckMessage),
		LastVerifiedFingerprint: cloneStringPtr(src.LastVerifiedFingerprint),
	}
}

func cloneExternalAPISettings(src *ExternalAPISettings) *ExternalAPISettings {
	if src == nil {
		return nil
	}
	out := &ExternalAPISettings{}
	if src.APIKeys != nil {
		out.APIKeys = make([]ExternalAPIKey, 0, len(src.APIKeys))
		for _, key := range src.APIKeys {
			cloned := key
			cloned.Scopes = cloneStringSlice(key.Scopes)
			cloned.AllowedThreadIDs = cloneStringSlice(key.AllowedThreadIDs)
			cloned.AllowedProjectIDs = cloneStringSlice(key.AllowedProjectIDs)
			cloned.LastUsedAt = cloneInt64Ptr(key.LastUsedAt)
			cloned.RevokedAt = cloneInt64Ptr(key.RevokedAt)
			out.APIKeys = append(out.APIKeys, cloned)
		}
	}
	return out
}

func ptrStringTrimmed(v string) *string {
	if trimmed := strings.TrimSpace(v); trimmed != "" {
		return &trimmed
	}
	return nil
}

func sanitizeDatasourceConnectionState(in *DatasourceConnectionState) *DatasourceConnectionState {
	off := false
	if in == nil {
		return &DatasourceConnectionState{Enabled: &off}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.LastProjectID != nil {
		v := strings.TrimSpace(*out.LastProjectID)
		if v == "" {
			out.LastProjectID = nil
		} else {
			out.LastProjectID = &v
		}
	}
	if out.LastHost != nil {
		v := strings.TrimSpace(*out.LastHost)
		if v == "" {
			out.LastHost = nil
		} else {
			out.LastHost = &v
		}
	}
	if out.LastWarehouseID != nil {
		v := strings.TrimSpace(*out.LastWarehouseID)
		if v == "" {
			out.LastWarehouseID = nil
		} else {
			out.LastWarehouseID = &v
		}
	}
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	if out.AutomaticScanBytes != nil && *out.AutomaticScanBytes <= 0 {
		out.AutomaticScanBytes = nil
	}
	if out.ApprovedScanBytes != nil && *out.ApprovedScanBytes <= 0 {
		out.ApprovedScanBytes = nil
	}
	if out.AutomaticScanBytes != nil && out.ApprovedScanBytes != nil &&
		*out.ApprovedScanBytes < *out.AutomaticScanBytes {
		value := *out.AutomaticScanBytes
		out.ApprovedScanBytes = &value
	}
	out.LastVerifiedFingerprint = ptrStringTrimmedPtr(out.LastVerifiedFingerprint)
	return &out
}

func sanitizeConnectionsSettings(in *ConnectionsSettings) *ConnectionsSettings {
	if in == nil {
		return &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{ConnectionCatalogThreadSearch}}
	}

	seen := map[ConnectionCatalogID]bool{}
	out := &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{}}
	for _, raw := range in.VisibleIDs {
		id, ok := normalizeConnectionCatalogID(raw)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out.VisibleIDs = append(out.VisibleIDs, id)
	}
	return out
}

func boolPtrTrue(in *bool) bool {
	return in != nil && *in
}

func appendVisibleConnectionID(in *ConnectionsSettings, id ConnectionCatalogID) *ConnectionsSettings {
	out := cloneConnectionsSettings(in)
	if out == nil {
		out = &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{}}
	}
	for _, existing := range out.VisibleIDs {
		if existing == id {
			return out
		}
	}
	out.VisibleIDs = append(out.VisibleIDs, id)
	return out
}

func ensureEnabledConnectionsVisible(in AppSettings) *ConnectionsSettings {
	out := cloneConnectionsSettings(in.Connections)
	if boolPtrTrue(in.ThreadSearch.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogThreadSearch)
	}
	if boolPtrTrue(in.ImageGeneration.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogImageGeneration)
	}
	for _, cfg := range in.ProviderConfigs {
		if !providerSupportsHostedConnections(cfg) {
			continue
		}
		if providerHostedConnectionEnabled(cfg, "hosted_web_search", cfg.WebSearch) {
			out = appendVisibleConnectionID(out, ConnectionCatalogWebSearch)
		}
		if providerHostedConnectionEnabled(cfg, "hosted_code_execution", cfg.CodeExecution) {
			out = appendVisibleConnectionID(out, ConnectionCatalogPythonSandbox)
		}
	}
	if boolPtrTrue(in.LocalComputer.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogLocalComputer)
	}
	if boolPtrTrue(in.BrowserControl.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogBrowser)
	}
	if boolPtrTrue(in.GitHub.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogGitHub)
	}
	if boolPtrTrue(in.Slack.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogSlack)
	}
	if boolPtrTrue(in.Atlassian.Jira.Enabled) || boolPtrTrue(in.Atlassian.Confluence.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogAtlassian)
	}
	if boolPtrTrue(in.SharePoint.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogSharePoint)
	}
	if boolPtrTrue(in.DataSources.BigQuery.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogBigQuery)
	}
	if boolPtrTrue(in.DataSources.Databricks.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogDatabricks)
	}
	if boolPtrTrue(in.DataSources.Trino.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogTrino)
	}
	if boolPtrTrue(in.DataSources.Snowflake.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogSnowflake)
	}
	if boolPtrTrue(in.Arxiv.Enabled) {
		out = appendVisibleConnectionID(out, ConnectionCatalogArxiv)
	}
	return out
}

func providerSupportsHostedConnections(cfg ProviderConfig) bool {
	providerID := strings.ToLower(strings.TrimSpace(cfg.ProviderID))
	surface := ""
	if cfg.APISurface != nil {
		surface = strings.ToLower(strings.TrimSpace(*cfg.APISurface))
	}
	if surface == "" {
		switch providerID {
		case "openai":
			surface = "responses"
		case "anthropic":
			surface = "messages"
		case "compatible":
			surface = "chat_completions"
			if cfg.ApiType != nil && strings.TrimSpace(*cfg.ApiType) != "" {
				surface = strings.ToLower(strings.TrimSpace(*cfg.ApiType))
			}
		}
	}
	return (providerID == "openai" && surface == "responses") ||
		(providerID == "anthropic" && surface == "messages") ||
		(providerID == "compatible" && surface == "responses")
}

func providerHostedConnectionEnabled(cfg ProviderConfig, controlKey string, mirror any) bool {
	control := strings.ToLower(strings.TrimSpace(cfg.CapabilityControls[controlKey]))
	if control != "" {
		return control == "on"
	}
	switch value := mirror.(type) {
	case *ProviderWebSearchConfig:
		return value != nil && boolPtrTrue(value.Enabled)
	case *ProviderCodeExecutionConfig:
		return value != nil && boolPtrTrue(value.Enabled)
	default:
		return false
	}
}

// reconcileGlobalWebSearchProviderConfigs collapses legacy per-provider Web
// Search values into the single app-level connection exposed by the UI. A
// previous app version could leave OpenAI On and Azure-compatible Responses
// Off; the catalogue then looked ready even though an Azure run omitted the
// provider-hosted tool. Sanitizing this on every load and save prevents stale
// settings files from reaching the provider factory.
func reconcileGlobalWebSearchProviderConfigs(configs []ProviderConfig) []ProviderConfig {
	enabled := false
	for _, cfg := range configs {
		if !providerSupportsHostedConnections(cfg) {
			continue
		}
		if providerHostedConnectionEnabled(cfg, "hosted_web_search", cfg.WebSearch) {
			enabled = true
		}
	}
	for i := range configs {
		cfg := configs[i]
		if !providerSupportsHostedConnections(cfg) {
			if cfg.CapabilityControls != nil {
				cfg.CapabilityControls["hosted_web_search"] = "off"
			}
			cfg.WebSearch = nil
			configs[i] = cfg
			continue
		}
		if cfg.CapabilityControls == nil {
			cfg.CapabilityControls = map[string]string{}
		}
		if enabled {
			cfg.CapabilityControls["hosted_web_search"] = "on"
		} else {
			cfg.CapabilityControls["hosted_web_search"] = "off"
		}
		cfg.WebSearch = canonicalWebSearchConfig(cfg.ProviderID, enabled)
		configs[i] = cfg
	}
	return configs
}

// canonicalizeLegacyWebSearchMirrors removes raw tool definitions persisted by
// older clients. When provider configs exist, their global value is
// authoritative. Legacy-only settings retain each mirror's enabled bit while
// still receiving the current provider-native request schema.
func canonicalizeLegacyWebSearchMirrors(out *AppSettings, globalEnabled *bool) {
	resolveEnabled := func(current *ProviderWebSearchConfig) bool {
		if globalEnabled != nil {
			return *globalEnabled
		}
		return current != nil && boolPtrTrue(current.Enabled)
	}

	if out.OpenAI != nil {
		out.OpenAI.WebSearch = canonicalWebSearchConfig("openai", resolveEnabled(out.OpenAI.WebSearch))
	}
	if out.Anthropic != nil {
		out.Anthropic.WebSearch = canonicalWebSearchConfig("anthropic", resolveEnabled(out.Anthropic.WebSearch))
	}
	for i := range out.CompatibleProfiles {
		profile := out.CompatibleProfiles[i]
		if strings.EqualFold(strings.TrimSpace(profile.ApiType), "responses") {
			profile.WebSearch = canonicalWebSearchConfig("compatible", resolveEnabled(profile.WebSearch))
		} else {
			profile.WebSearch = nil
		}
		out.CompatibleProfiles[i] = profile
	}
}

func sanitizeThreadSearchSettings(in *ThreadSearchSettings) *ThreadSearchSettings {
	on := true
	if in == nil {
		return &ThreadSearchSettings{
			Enabled: &on,
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &on
	}
	return &out
}

func sanitizeLocalComputerSettings(in *LocalComputerSettings) *LocalComputerSettings {
	off := false
	wholeComputer := LocalComputerAccessModeWholeComputer
	if in == nil {
		return &LocalComputerSettings{
			Enabled:         &off,
			AccessMode:      &wholeComputer,
			SelectedFolders: []string{},
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	mode := LocalComputerAccessModeWholeComputer
	if out.AccessMode != nil {
		switch strings.ToLower(strings.TrimSpace(string(*out.AccessMode))) {
		case string(LocalComputerAccessModeSelectedFolders):
			mode = LocalComputerAccessModeSelectedFolders
		}
	}
	out.AccessMode = &mode
	if mode == LocalComputerAccessModeSelectedFolders {
		out.SelectedFolders = normalizeTrimmedStringSlice(out.SelectedFolders)
	} else {
		out.SelectedFolders = []string{}
	}
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	return &out
}

func normalizeConnectionCatalogID(raw ConnectionCatalogID) (ConnectionCatalogID, bool) {
	id := ConnectionCatalogID(strings.ToLower(strings.TrimSpace(string(raw))))
	switch id {
	case ConnectionCatalogThreadSearch,
		ConnectionCatalogImageGeneration,
		ConnectionCatalogWebSearch,
		ConnectionCatalogPythonSandbox,
		ConnectionCatalogLocalComputer,
		ConnectionCatalogBrowser,
		ConnectionCatalogGitHub,
		ConnectionCatalogSlack,
		ConnectionCatalogAtlassian,
		ConnectionCatalogSharePoint,
		ConnectionCatalogArxiv,
		ConnectionCatalogBigQuery,
		ConnectionCatalogDatabricks,
		ConnectionCatalogTrino,
		ConnectionCatalogSnowflake:
		return id, true
	default:
		return "", false
	}
}

func sanitizeAtlassianProductSettings(in *AtlassianProductSettings) *AtlassianProductSettings {
	off := false
	showBrowser := false
	if in == nil {
		return &AtlassianProductSettings{
			Enabled:     &off,
			ShowBrowser: &showBrowser,
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ShowBrowser == nil {
		out.ShowBrowser = &showBrowser
	}
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	return &out
}

func sanitizeAtlassianSettings(in *AtlassianSettings) *AtlassianSettings {
	defaults := DefaultSettings().Atlassian
	if in == nil {
		return defaults.CloneAtlassian()
	}
	out := *in
	out.JiraBaseURL = normalizeAtlassianURLPtr(out.JiraBaseURL, false)
	out.ConfluenceBaseURL = normalizeAtlassianURLPtr(out.ConfluenceBaseURL, true)
	if out.ConfluenceBaseURL == nil && out.JiraBaseURL != nil {
		base := strings.TrimRight(strings.TrimSpace(*out.JiraBaseURL), "/") + "/wiki"
		out.ConfluenceBaseURL = &base
	}
	if out.DefaultJiraProjectKey != nil {
		v := strings.TrimSpace(strings.ToUpper(*out.DefaultJiraProjectKey))
		if v == "" {
			out.DefaultJiraProjectKey = nil
		} else {
			out.DefaultJiraProjectKey = &v
		}
	}
	if out.BrowserProfileDir != nil {
		v := strings.TrimSpace(*out.BrowserProfileDir)
		if v == "" {
			out.BrowserProfileDir = nil
		} else {
			out.BrowserProfileDir = &v
		}
	}
	out.BrowserProvider = nil
	if defaults != nil && defaults.BrowserProvider != nil {
		out.BrowserProvider = cloneStringPtr(defaults.BrowserProvider)
	}
	out.BrowserChannel = nil
	out.BrowserExecutablePath = nil
	out.AllowedProjects = normalizeUpperStringSlice(out.AllowedProjects)
	out.AllowedAuthDomains = normalizeLowerStringSlice(out.AllowedAuthDomains)
	if len(out.AllowedAuthDomains) == 0 && defaults != nil {
		out.AllowedAuthDomains = cloneStringSlice(defaults.AllowedAuthDomains)
	}
	out.LastVerifiedFingerprint = ptrStringTrimmedPtr(out.LastVerifiedFingerprint)
	out.Jira = sanitizeAtlassianProductSettings(out.Jira)
	out.Confluence = sanitizeAtlassianProductSettings(out.Confluence)
	return &out
}

func sanitizeBrowserControlSettings(in *BrowserControlSettings) *BrowserControlSettings {
	off := false
	showBrowser := false
	if in == nil {
		return &BrowserControlSettings{
			Enabled:     &off,
			ShowBrowser: &showBrowser,
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ShowBrowser == nil {
		out.ShowBrowser = &showBrowser
	}
	if out.BrowserProfileDir != nil {
		v := strings.TrimSpace(*out.BrowserProfileDir)
		if v == "" {
			out.BrowserProfileDir = nil
		} else {
			out.BrowserProfileDir = &v
		}
	}
	return &out
}

func sanitizeSharePointSettings(in *SharePointSettings) *SharePointSettings {
	off := false
	showBrowser := false
	authMode := "browser"
	if in == nil {
		return &SharePointSettings{
			Enabled:     &off,
			ShowBrowser: &showBrowser,
			AuthMode:    &authMode,
		}
	}
	out := *in
	out.SiteURL = normalizeSharePointURLPtr(out.SiteURL)
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ShowBrowser == nil {
		out.ShowBrowser = &showBrowser
	}
	if out.BrowserProfileDir != nil {
		v := strings.TrimSpace(*out.BrowserProfileDir)
		if v == "" {
			out.BrowserProfileDir = nil
		} else {
			out.BrowserProfileDir = &v
		}
	}
	if out.AuthMode != nil && strings.EqualFold(strings.TrimSpace(*out.AuthMode), "graph") {
		mode := "graph"
		out.AuthMode = &mode
	} else {
		out.AuthMode = &authMode
	}
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	out.LastVerifiedFingerprint = ptrStringTrimmedPtr(out.LastVerifiedFingerprint)
	return &out
}

func sanitizeGitHubSettings(in *GitHubSettings) *GitHubSettings {
	off := false
	if in == nil {
		return &GitHubSettings{
			Enabled:             &off,
			ReadTools:           &off,
			AllowedRepositories: []string{},
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ReadTools == nil {
		out.ReadTools = cloneBoolPtr(out.Enabled)
	}
	out.AllowedRepositories = normalizeGitHubRepositorySlice(out.AllowedRepositories)
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	out.LastVerifiedFingerprint = ptrStringTrimmedPtr(out.LastVerifiedFingerprint)
	return &out
}

func sanitizeSlackSettings(in *SlackSettings) *SlackSettings {
	off := false
	writeIdentity := "user"
	if in == nil {
		return &SlackSettings{
			Enabled:           &off,
			ReadTools:         &off,
			WriteTools:        &off,
			WriteIdentity:     &writeIdentity,
			AllowedTeamIDs:    []string{},
			AllowedChannelIDs: []string{},
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ReadTools == nil {
		out.ReadTools = cloneBoolPtr(out.Enabled)
	}
	if out.WriteTools == nil {
		out.WriteTools = &off
	}
	if out.WriteIdentity != nil && strings.TrimSpace(strings.ToLower(*out.WriteIdentity)) == "bot" {
		writeIdentity = "bot"
	}
	out.WriteIdentity = &writeIdentity
	out.AllowedTeamIDs = normalizeUpperStringSlice(out.AllowedTeamIDs)
	out.AllowedChannelIDs = normalizeUpperStringSlice(out.AllowedChannelIDs)
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	out.LastVerifiedFingerprint = ptrStringTrimmedPtr(out.LastVerifiedFingerprint)
	return &out
}

func sanitizeArxivSettings(in *ArxivSettings) *ArxivSettings {
	off := false
	if in == nil {
		return &ArxivSettings{
			Enabled: &off,
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ContactEmail != nil {
		out.ContactEmail = ptrStringTrimmed(*out.ContactEmail)
	}
	if out.LastCheckMessage != nil {
		out.LastCheckMessage = ptrStringTrimmed(*out.LastCheckMessage)
	}
	return &out
}

func sanitizeImageGenerationSettings(in *ImageGenerationSettings) *ImageGenerationSettings {
	off := false
	if in == nil {
		return &ImageGenerationSettings{
			Enabled: &off,
		}
	}
	out := *in
	if out.Enabled == nil {
		out.Enabled = &off
	}
	if out.ProviderID != nil {
		out.ProviderID = ptrStringTrimmed(*out.ProviderID)
	}
	if out.Model != nil {
		out.Model = ptrStringTrimmed(*out.Model)
	}
	if out.Defaults != nil {
		defaults := *out.Defaults
		if defaults.Size != nil {
			defaults.Size = ptrStringTrimmed(*defaults.Size)
		}
		if defaults.Quality != nil {
			defaults.Quality = ptrStringTrimmed(*defaults.Quality)
		}
		if defaults.Format != nil {
			defaults.Format = ptrStringTrimmed(*defaults.Format)
		}
		out.Defaults = &defaults
	}
	if out.MaxImagesPerRun != nil {
		clamped := *out.MaxImagesPerRun
		if clamped < 1 {
			clamped = 1
		} else if clamped > 4 {
			clamped = 4
		}
		out.MaxImagesPerRun = &clamped
	}
	return &out
}

func sanitizeExternalAPISettings(in *ExternalAPISettings) *ExternalAPISettings {
	if in == nil {
		return &ExternalAPISettings{APIKeys: []ExternalAPIKey{}}
	}
	out := cloneExternalAPISettings(in)
	if out.APIKeys == nil {
		out.APIKeys = []ExternalAPIKey{}
	}
	normalized := make([]ExternalAPIKey, 0, len(out.APIKeys))
	for _, key := range out.APIKeys {
		key.ID = strings.TrimSpace(key.ID)
		key.Name = strings.TrimSpace(key.Name)
		key.Prefix = strings.TrimSpace(key.Prefix)
		key.Hash = strings.ToLower(strings.TrimSpace(key.Hash))
		key.Scopes = normalizeLowerStringSlice(key.Scopes)
		key.AllowedThreadIDs = normalizeTrimmedStringSlice(key.AllowedThreadIDs)
		key.AllowedProjectIDs = normalizeTrimmedStringSlice(key.AllowedProjectIDs)
		if key.ID == "" && key.Hash == "" {
			continue
		}
		normalized = append(normalized, key)
	}
	out.APIKeys = normalized
	return out
}

func (s *AtlassianSettings) CloneAtlassian() *AtlassianSettings {
	if s == nil {
		return nil
	}
	out := *s
	out.JiraBaseURL = cloneStringPtr(s.JiraBaseURL)
	out.ConfluenceBaseURL = cloneStringPtr(s.ConfluenceBaseURL)
	out.DefaultJiraProjectKey = cloneStringPtr(s.DefaultJiraProjectKey)
	out.BrowserProvider = cloneStringPtr(s.BrowserProvider)
	out.BrowserChannel = cloneStringPtr(s.BrowserChannel)
	out.BrowserExecutablePath = cloneStringPtr(s.BrowserExecutablePath)
	out.BrowserProfileDir = cloneStringPtr(s.BrowserProfileDir)
	out.LastVerifiedFingerprint = cloneStringPtr(s.LastVerifiedFingerprint)
	out.AllowedProjects = cloneStringSlice(s.AllowedProjects)
	out.AllowedAuthDomains = cloneStringSlice(s.AllowedAuthDomains)
	out.Jira = cloneAtlassianProductSettings(s.Jira)
	out.Confluence = cloneAtlassianProductSettings(s.Confluence)
	return &out
}

func ptrStringTrimmedPtr(src *string) *string {
	if src == nil {
		return nil
	}
	return ptrStringTrimmed(*src)
}

func normalizeAtlassianURLPtr(src *string, confluence bool) *string {
	if src == nil {
		return nil
	}
	value := strings.TrimSpace(*src)
	if value == "" {
		return nil
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.TrimSpace(parsed.Host) != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		parsed.RawQuery = ""
		parsed.Fragment = ""
		if confluence && parsed.Path == "" && strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".atlassian.net") {
			parsed.Path = "/wiki"
		}
		value = strings.TrimRight(parsed.String(), "/")
	} else {
		value = strings.TrimRight(value, "/")
	}
	if value == "" {
		return nil
	}
	return &value
}

func normalizeSharePointURLPtr(src *string) *string {
	if src == nil {
		return nil
	}
	value := strings.TrimSpace(*src)
	if value == "" {
		return nil
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.TrimSpace(parsed.Host) != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		parsed.RawQuery = ""
		parsed.Fragment = ""
		value = strings.TrimRight(parsed.String(), "/")
	} else {
		value = strings.TrimRight(value, "/")
	}
	if value == "" {
		return nil
	}
	return &value
}

func normalizeUpperStringSlice(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		v := strings.TrimSpace(strings.ToUpper(value))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func normalizeLowerStringSlice(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		v := strings.TrimSpace(strings.ToLower(value))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func normalizeTrimmedStringSlice(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func normalizeGitHubRepositorySlice(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		v := strings.TrimSpace(strings.ToLower(value))
		v = strings.TrimPrefix(v, "ssh://git@github.com/")
		v = strings.TrimPrefix(v, "https://github.com/")
		v = strings.TrimPrefix(v, "http://github.com/")
		v = strings.TrimPrefix(v, "git@github.com:")
		v = strings.TrimSuffix(v, ".git")
		v = strings.Trim(v, "/")
		parts := strings.Split(v, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		v = parts[0] + "/" + parts[1]
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func showAllProvidersEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("AIA_SHOW_ALL_PROVIDERS")))
	switch raw {
	case "1", "true", "t", "yes", "on":
		return true
	case "", "0", "false", "f", "no", "off":
		return false
	default:
		return false
	}
}

func defaultFeatureFlags() *FeatureFlags {
	activationEnabled := true
	chartProgramPipelineEnabled := true
	allProvidersEnabled := defaultShowAllProvidersEnabled() || showAllProvidersEnabled()
	cloudCredentialsEnabled := defaultShowAllProvidersEnabled()
	chartEngines := &ChartEngineFeatureFlags{
		Echarts:    true,
		Highcharts: false,
	}
	return &FeatureFlags{
		Activation: &activationEnabled,
		Providers: ProviderFeatureFlags{
			OpenAI:     allProvidersEnabled,
			Anthropic:  allProvidersEnabled,
			Gemini:     allProvidersEnabled,
			Bedrock:    true,
			Compatible: true,
		},
		ProviderTemplates:         true,
		CloudCredentialsAWS:       cloudCredentialsEnabled,
		CloudCredentialsTrino:     cloudCredentialsEnabled,
		CloudCredentialsSnowflake: cloudCredentialsEnabled,
		ChartDimensionFilters:     false,
		ChartProgramPipeline:      &chartProgramPipelineEnabled,
		ChartEngines:              chartEngines,
	}
}

const legacyBedrockCompactionInstruction = "Focus on creating a detailed handover of where you previously left off what your core objective was, what your major findings were, and what you have remaining to solve, plus any useful context for supporting this"
const defaultBedrockCompactionInstruction = "Create a compact but complete handover summary. Include the latest user request, confirmed findings, key evidence (queries, jobs, artifacts, and message references), decisions made, and remaining next steps. Preserve critical context needed to continue accurately. Do not draft or continue the user-facing reply; output summary content only."
const defaultCompactionTriggerTokens = 170000
const defaultOpenAICompactionTriggerTokens = 200000
const minCompactionTriggerTokens = 50000
const DefaultDatabaseQueryRowLimit = 2500
const MaxDatabaseQueryRowLimit = 2500

const (
	DefaultDatabaseQueryMaxExecutingGlobal               = 8
	DefaultDatabaseQueryMaxExecutingPerThread            = 3
	DefaultDatabaseQueryMaxExecutingPerConnection        = 2
	DefaultDatabaseQueryInlineWaitMilliseconds           = int64(90_000)
	DefaultDatabaseQueryMaxPollIntervalMilliseconds      = int64(30_000)
	DefaultDatabaseQueryMonitorErrorBackoffMilliseconds  = int64(120_000)
	DefaultDatabaseQuerySoftQueueTimeoutMilliseconds     = int64(600_000)
	DefaultDatabaseQuerySoftExecutionTimeoutMilliseconds = int64(1_800_000)
	DefaultDatabaseQueryHardExecutionTimeoutMilliseconds = int64(21_600_000)
	DefaultDatabaseQueryMaxMaterializedBytes             = int64(32 * 1024 * 1024)
	DefaultBigQueryMaximumBytesBilled                    = int64(100 * 1024 * 1024 * 1024)
	DefaultDatabaseQueryMaxAutoContinuations             = 8

	// Backend safety bounds. Persisted settings are untrusted input: tiny
	// intervals can hammer coordinators while unbounded concurrency can turn a
	// single model turn into a warehouse-wide fan-out.
	MinDatabaseQueryInlineWaitMilliseconds           = int64(1_000)
	MinDatabaseQueryMaxPollIntervalMilliseconds      = int64(1_000)
	MinDatabaseQueryMonitorErrorBackoffMilliseconds  = int64(5_000)
	MinDatabaseQuerySoftQueueTimeoutMilliseconds     = int64(30_000)
	MinDatabaseQuerySoftExecutionTimeoutMilliseconds = int64(60_000)
	MinDatabaseQueryHardExecutionTimeoutMilliseconds = int64(60_000)
	MaxDatabaseQueryExecutingGlobal                  = 64
	MaxDatabaseQueryExecutingPerThread               = 16
	MaxDatabaseQueryExecutingPerConnection           = 16
	MaxDatabaseQueryAutoContinuations                = 32
)

func DefaultDatabaseQueryPolicy() DatabaseQueryPolicy {
	return DatabaseQueryPolicy{
		MaxExecutingGlobal:               DefaultDatabaseQueryMaxExecutingGlobal,
		MaxExecutingPerThread:            DefaultDatabaseQueryMaxExecutingPerThread,
		MaxExecutingPerConnection:        DefaultDatabaseQueryMaxExecutingPerConnection,
		InlineWaitMilliseconds:           DefaultDatabaseQueryInlineWaitMilliseconds,
		MaxPollIntervalMilliseconds:      DefaultDatabaseQueryMaxPollIntervalMilliseconds,
		MonitorErrorBackoffMilliseconds:  DefaultDatabaseQueryMonitorErrorBackoffMilliseconds,
		SoftQueueTimeoutMilliseconds:     DefaultDatabaseQuerySoftQueueTimeoutMilliseconds,
		SoftExecutionTimeoutMilliseconds: DefaultDatabaseQuerySoftExecutionTimeoutMilliseconds,
		HardExecutionTimeoutMilliseconds: DefaultDatabaseQueryHardExecutionTimeoutMilliseconds,
		MaxResultRows:                    DefaultDatabaseQueryRowLimit,
		MaxMaterializedBytes:             DefaultDatabaseQueryMaxMaterializedBytes,
		BigQueryMaximumBytesBilled:       DefaultBigQueryMaximumBytesBilled,
		MaxAutoContinuations:             DefaultDatabaseQueryMaxAutoContinuations,
	}
}

func sanitizeDatabaseQueryPolicy(in *DatabaseQueryPolicy) *DatabaseQueryPolicy {
	defaults := DefaultDatabaseQueryPolicy()
	if in == nil {
		return &defaults
	}
	out := *in
	positiveInt := func(value, fallback int) int {
		if value <= 0 {
			return fallback
		}
		return value
	}
	positiveInt64 := func(value, fallback int64) int64 {
		if value <= 0 {
			return fallback
		}
		return value
	}
	clampInt := func(value, minimum, maximum int) int {
		if value < minimum {
			return minimum
		}
		if value > maximum {
			return maximum
		}
		return value
	}
	clampInt64 := func(value, minimum int64) int64 {
		if value < minimum {
			return minimum
		}
		return value
	}
	out.MaxExecutingGlobal = positiveInt(out.MaxExecutingGlobal, defaults.MaxExecutingGlobal)
	out.MaxExecutingGlobal = clampInt(out.MaxExecutingGlobal, 1, MaxDatabaseQueryExecutingGlobal)
	out.MaxExecutingPerThread = positiveInt(out.MaxExecutingPerThread, defaults.MaxExecutingPerThread)
	out.MaxExecutingPerThread = clampInt(out.MaxExecutingPerThread, 1, MaxDatabaseQueryExecutingPerThread)
	if out.MaxExecutingPerThread > out.MaxExecutingGlobal {
		out.MaxExecutingPerThread = out.MaxExecutingGlobal
	}
	out.MaxExecutingPerConnection = positiveInt(out.MaxExecutingPerConnection, defaults.MaxExecutingPerConnection)
	out.MaxExecutingPerConnection = clampInt(out.MaxExecutingPerConnection, 1, MaxDatabaseQueryExecutingPerConnection)
	if out.MaxExecutingPerConnection > out.MaxExecutingPerThread {
		out.MaxExecutingPerConnection = out.MaxExecutingPerThread
	}
	out.InlineWaitMilliseconds = positiveInt64(out.InlineWaitMilliseconds, defaults.InlineWaitMilliseconds)
	out.InlineWaitMilliseconds = clampInt64(out.InlineWaitMilliseconds, MinDatabaseQueryInlineWaitMilliseconds)
	out.MaxPollIntervalMilliseconds = positiveInt64(out.MaxPollIntervalMilliseconds, defaults.MaxPollIntervalMilliseconds)
	out.MaxPollIntervalMilliseconds = clampInt64(out.MaxPollIntervalMilliseconds, MinDatabaseQueryMaxPollIntervalMilliseconds)
	out.MonitorErrorBackoffMilliseconds = positiveInt64(out.MonitorErrorBackoffMilliseconds, defaults.MonitorErrorBackoffMilliseconds)
	out.MonitorErrorBackoffMilliseconds = clampInt64(out.MonitorErrorBackoffMilliseconds, MinDatabaseQueryMonitorErrorBackoffMilliseconds)
	if out.MonitorErrorBackoffMilliseconds < out.MaxPollIntervalMilliseconds {
		out.MonitorErrorBackoffMilliseconds = out.MaxPollIntervalMilliseconds
	}
	out.SoftQueueTimeoutMilliseconds = positiveInt64(out.SoftQueueTimeoutMilliseconds, defaults.SoftQueueTimeoutMilliseconds)
	out.SoftQueueTimeoutMilliseconds = clampInt64(out.SoftQueueTimeoutMilliseconds, MinDatabaseQuerySoftQueueTimeoutMilliseconds)
	out.SoftExecutionTimeoutMilliseconds = positiveInt64(out.SoftExecutionTimeoutMilliseconds, defaults.SoftExecutionTimeoutMilliseconds)
	out.SoftExecutionTimeoutMilliseconds = clampInt64(out.SoftExecutionTimeoutMilliseconds, MinDatabaseQuerySoftExecutionTimeoutMilliseconds)
	out.HardExecutionTimeoutMilliseconds = positiveInt64(out.HardExecutionTimeoutMilliseconds, defaults.HardExecutionTimeoutMilliseconds)
	out.HardExecutionTimeoutMilliseconds = clampInt64(out.HardExecutionTimeoutMilliseconds, MinDatabaseQueryHardExecutionTimeoutMilliseconds)
	if out.HardExecutionTimeoutMilliseconds < out.SoftExecutionTimeoutMilliseconds {
		out.HardExecutionTimeoutMilliseconds = out.SoftExecutionTimeoutMilliseconds
	}
	out.MaxResultRows = positiveInt(out.MaxResultRows, defaults.MaxResultRows)
	if out.MaxResultRows > MaxDatabaseQueryRowLimit {
		out.MaxResultRows = MaxDatabaseQueryRowLimit
	}
	out.MaxMaterializedBytes = positiveInt64(out.MaxMaterializedBytes, defaults.MaxMaterializedBytes)
	out.BigQueryMaximumBytesBilled = positiveInt64(out.BigQueryMaximumBytesBilled, defaults.BigQueryMaximumBytesBilled)
	out.MaxAutoContinuations = positiveInt(out.MaxAutoContinuations, defaults.MaxAutoContinuations)
	out.MaxAutoContinuations = clampInt(out.MaxAutoContinuations, 1, MaxDatabaseQueryAutoContinuations)
	return &out
}

func DefaultSettings() AppSettings {
	anthropicVersion := "2023-06-01"
	openaiBase := "https://api.openai.com/v1"
	anthropicBase := "https://api.anthropic.com"
	geminiBase := "https://generativelanguage.googleapis.com"
	bedrockRegion := "us-east-1"
	bedrockAnthropicVersion := "bedrock-2023-05-31"
	openAICompactionEnabled := true
	openAICompactionTrigger := defaultOpenAICompactionTriggerTokens
	anthropicCompactionEnabled := true
	anthropicCompactionInstruction := defaultBedrockCompactionInstruction
	anthropicCompactionTrigger := defaultCompactionTriggerTokens
	bedrockCompactionEnabled := true
	bedrockCompactionInstruction := defaultBedrockCompactionInstruction
	bedrockCompactionTrigger := defaultCompactionTriggerTokens

	systemPrompt := `You are a helpful, witty, and precise AI assistant. 
  
If the user uploads a directory/files, you have access to tools to read them.
- Use 'list_files' to explore the directory structure first.
- Use 'search_files' to find specific code patterns or text.
- Use 'read_file' to examine specific file contents.
- Always check the file structure before assuming file paths exists.
- Batch tool calls when possible (e.g., request multiple read_file calls in one step), and avoid repeating the same tool call with identical arguments.

When presenting data, use Markdown tables if the data is structured.

For custom interactive visuals or small browser-based tools:
- Use 'create_interactive_artifact' to render self-contained HTML/CSS/JavaScript in a sandboxed iframe.
- When the visual depends on dataset rows, use 'create_interactive_data_artifact' with source.artifact_id instead of embedding rows in HTML or tool arguments. In the HTML, call window.sandboxData.getMetadata() and window.sandboxData.getRows({ offset, limit, columns }) for focused pages.
- Keep the artifact standalone; external network requests, external scripts, external stylesheets, cookies, and app API access are blocked.
- Prefer display_table/build_chart_artifact for ordinary data tables and charts; use create_interactive_artifact when bespoke interactivity is needed.

Tool results are returned as JSON envelopes. Read the actual tool output from the "result" field.
- artifact_id: stable UUID for this tool result (can be referenced later)
- ok: whether the tool succeeded
- result_format: "json" or "text"
- result: the tool output payload.

When the user references artifacts (marked with "📎 Referenced Artifacts"), they are asking about those specific tool results. A content preview is included — for large datasets, only the first rows are shown. Treat tool-result envelopes as context previews, not full datasets. Use artifact_id plus paging/retrieval tools when you need more rows, and do not request inline full-data payloads to bypass compaction.

When the user references message blocks (marked with "Referenced Message Blocks"), treat those snapshots as focused context from prior user/assistant/tool content. Use the block metadata (role, message_id/seq, block key) to ground follow-up answers and avoid rehashing unrelated history.`

	maxIters := 200
	queryRowLimit := DefaultDatabaseQueryRowLimit
	databaseQueryPolicy := DefaultDatabaseQueryPolicy()
	defaultDsDisabledBigQuery := false
	defaultDsDisabledDatabricks := false
	defaultDsDisabledTrino := false
	defaultDsDisabledSnowflake := false
	defaultDsState := &DatasourceConnections{
		BigQuery: &DatasourceConnectionState{
			Enabled: &defaultDsDisabledBigQuery,
		},
		Databricks: &DatasourceConnectionState{
			Enabled: &defaultDsDisabledDatabricks,
		},
		Trino: &DatasourceConnectionState{
			Enabled: &defaultDsDisabledTrino,
		},
		Snowflake: &DatasourceConnectionState{
			Enabled: &defaultDsDisabledSnowflake,
		},
	}
	atlassianDisabled := false
	atlassianShowBrowser := false
	atlassianBrowserProvider := "bundled"
	browserControlDisabled := false
	browserControlShowBrowser := false
	sharePointDisabled := false
	sharePointShowBrowser := false
	sharePointAuthMode := "browser"
	githubDisabled := false
	githubReadToolsDisabled := false
	slackDisabled := false
	slackReadToolsDisabled := false
	slackWriteToolsDisabled := false
	slackWriteIdentity := "user"
	arxivDisabled := false
	imageGenerationDisabled := false
	imageGenerationModel := "gpt-image-2"
	imageGenerationSize := "auto"
	imageGenerationQuality := "auto"
	imageGenerationFormat := "png"
	imageGenerationMaxPerRun := 1
	threadSearchEnabled := true
	localComputerDisabled := false
	localComputerAccessMode := LocalComputerAccessModeWholeComputer

	return AppSettings{
		Provider: ProviderSelection{
			ProviderID: "compatible",
			Model:      "",
			ProfileID:  strptr("default"),
		},
		CompatibleProfiles: []CompatibleProfile{
			{
				ID:          "default",
				DisplayName: "OpenAI Compatible (Chat Completions)",
				ApiType:     "chat_completions",
				BaseURL:     openaiBase,
				Auth: ProviderAuth{
					Type:   "api_key",
					Header: "Authorization",
					Scheme: strptr("Bearer"),
				},
				ApiKey:    strptr(""),
				Extra:     []HeaderKV{},
				Query:     nil,
				Overrides: nil,
				TimeoutMs: nil,
			},
		},
		OpenAI: &OpenAIConfig{
			ApiKey:            "",
			BaseURL:           &openaiBase,
			CompactionEnabled: &openAICompactionEnabled,
			CompactionTrigger: &openAICompactionTrigger,
			WebSearch:         defaultWebSearchConfig("openai"),
		},
		Anthropic: &AnthropicConfig{
			ApiKey:                "",
			BaseURL:               &anthropicBase,
			AnthropicVersion:      &anthropicVersion,
			CompactionEnabled:     &anthropicCompactionEnabled,
			CompactionInstruction: &anthropicCompactionInstruction,
			CompactionTrigger:     &anthropicCompactionTrigger,
			WebSearch:             defaultWebSearchConfig("anthropic"),
		},
		Gemini: &GeminiConfig{
			ApiKey:  "",
			BaseURL: &geminiBase,
		},
		Bedrock: &BedrockConfig{
			Region:                bedrockRegion,
			BaseURL:               nil,
			AnthropicVersion:      &bedrockAnthropicVersion,
			CompactionEnabled:     &bedrockCompactionEnabled,
			CompactionInstruction: &bedrockCompactionInstruction,
			CompactionTrigger:     &bedrockCompactionTrigger,
			Extra:                 []HeaderKV{},
		},
		FeatureFlags: defaultFeatureFlags(),
		CloudCredentials: &CloudCredentials{
			AWS: []AWSCloudCredential{},
			GCP: []GCPCloudCredential{},
		},
		ProviderConfigs:        []ProviderConfig{},
		ProviderTemplates:      []ProviderTemplate{},
		ActiveProviderConfigID: nil,
		SystemPrompt:           systemPrompt,
		DataSources:            defaultDsState,
		Connections:            &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{ConnectionCatalogThreadSearch}},
		ThreadSearch:           &ThreadSearchSettings{Enabled: &threadSearchEnabled},
		LocalComputer: &LocalComputerSettings{
			Enabled:         &localComputerDisabled,
			AccessMode:      &localComputerAccessMode,
			SelectedFolders: []string{},
		},
		Atlassian: &AtlassianSettings{
			AllowedAuthDomains: []string{"id.atlassian.com", "auth.atlassian.com", "login.microsoftonline.com"},
			BrowserProvider:    &atlassianBrowserProvider,
			Jira: &AtlassianProductSettings{
				Enabled:     &atlassianDisabled,
				ShowBrowser: &atlassianShowBrowser,
			},
			Confluence: &AtlassianProductSettings{
				Enabled:     &atlassianDisabled,
				ShowBrowser: &atlassianShowBrowser,
			},
		},
		BrowserControl: &BrowserControlSettings{
			Enabled:     &browserControlDisabled,
			ShowBrowser: &browserControlShowBrowser,
		},
		SharePoint: &SharePointSettings{
			Enabled:     &sharePointDisabled,
			ShowBrowser: &sharePointShowBrowser,
			AuthMode:    &sharePointAuthMode,
		},
		GitHub: &GitHubSettings{
			Enabled:             &githubDisabled,
			ReadTools:           &githubReadToolsDisabled,
			AllowedRepositories: []string{},
		},
		Slack: &SlackSettings{
			Enabled:           &slackDisabled,
			ReadTools:         &slackReadToolsDisabled,
			WriteTools:        &slackWriteToolsDisabled,
			WriteIdentity:     &slackWriteIdentity,
			AllowedTeamIDs:    []string{},
			AllowedChannelIDs: []string{},
		},
		Arxiv: &ArxivSettings{
			Enabled: &arxivDisabled,
		},
		ImageGeneration: &ImageGenerationSettings{
			Enabled: &imageGenerationDisabled,
			Model:   &imageGenerationModel,
			Defaults: &ImageGenerationDefaults{
				Size:    &imageGenerationSize,
				Quality: &imageGenerationQuality,
				Format:  &imageGenerationFormat,
			},
			MaxImagesPerRun: &imageGenerationMaxPerRun,
		},
		ExternalAPI:           &ExternalAPISettings{APIKeys: []ExternalAPIKey{}},
		MaxToolIterations:     &maxIters,
		DatabaseQueryRowLimit: &queryRowLimit,
		DatabaseQueryPolicy:   &databaseQueryPolicy,
	}
}

func sanitizeSettings(in AppSettings) AppSettings {
	out := in.Clone()
	legacyQueryPolicyMissing := out.DatabaseQueryPolicy == nil
	if strings.EqualFold(strings.TrimSpace(out.Provider.ProviderID), "bedrock") {
		out.Provider.Model = normalizeBedrockModelID(out.Provider.Model)
	}

	if out.FeatureFlags == nil {
		out.FeatureFlags = defaultFeatureFlags()
	} else if out.FeatureFlags.Activation == nil {
		on := true
		out.FeatureFlags.Activation = &on
	}
	if out.FeatureFlags != nil && out.FeatureFlags.ChartEngines == nil {
		featureDefaults := defaultFeatureFlags()
		if featureDefaults != nil && featureDefaults.ChartEngines != nil {
			out.FeatureFlags.ChartEngines = cloneChartEngineFeatureFlagsPtr(featureDefaults.ChartEngines)
		}
	}
	if out.FeatureFlags != nil && out.FeatureFlags.ChartProgramPipeline == nil {
		featureDefaults := defaultFeatureFlags()
		if featureDefaults != nil {
			out.FeatureFlags.ChartProgramPipeline = cloneBoolPtr(featureDefaults.ChartProgramPipeline)
		}
	}
	// Build-scoped flags are enforced per build profile.
	featureDefaults := defaultFeatureFlags()
	if featureDefaults != nil {
		out.FeatureFlags.CloudCredentialsAWS = featureDefaults.CloudCredentialsAWS
		out.FeatureFlags.CloudCredentialsTrino = featureDefaults.CloudCredentialsTrino
		out.FeatureFlags.CloudCredentialsSnowflake = featureDefaults.CloudCredentialsSnowflake
	}
	if out.CloudCredentials == nil {
		out.CloudCredentials = &CloudCredentials{
			AWS: []AWSCloudCredential{},
			GCP: []GCPCloudCredential{},
		}
	}
	if out.ProviderConfigs == nil {
		out.ProviderConfigs = []ProviderConfig{}
	}
	if out.ProviderTemplates == nil {
		out.ProviderTemplates = []ProviderTemplate{}
	}
	if out.DataSources == nil {
		out.DataSources = &DatasourceConnections{}
	}
	out.DataSources.BigQuery = sanitizeDatasourceConnectionState(out.DataSources.BigQuery)
	out.DataSources.Databricks = sanitizeDatasourceConnectionState(out.DataSources.Databricks)
	out.DataSources.Trino = sanitizeDatasourceConnectionState(out.DataSources.Trino)
	out.DataSources.Snowflake = sanitizeDatasourceConnectionState(out.DataSources.Snowflake)
	out.Connections = sanitizeConnectionsSettings(out.Connections)
	out.ThreadSearch = sanitizeThreadSearchSettings(out.ThreadSearch)
	out.LocalComputer = sanitizeLocalComputerSettings(out.LocalComputer)
	out.Atlassian = sanitizeAtlassianSettings(out.Atlassian)
	out.BrowserControl = sanitizeBrowserControlSettings(out.BrowserControl)
	out.SharePoint = sanitizeSharePointSettings(out.SharePoint)
	out.GitHub = sanitizeGitHubSettings(out.GitHub)
	out.Slack = sanitizeSlackSettings(out.Slack)
	out.Arxiv = sanitizeArxivSettings(out.Arxiv)
	out.ImageGeneration = sanitizeImageGenerationSettings(out.ImageGeneration)
	out.ExternalAPI = sanitizeExternalAPISettings(out.ExternalAPI)
	out.Connections = ensureEnabledConnectionsVisible(out)

	// Never persist secrets in the plain settings file.
	if out.OpenAI != nil {
		if out.OpenAI.CompactionEnabled == nil {
			enabled := true
			out.OpenAI.CompactionEnabled = &enabled
		}
		out.OpenAI.CompactionTrigger = normalizeOpenAICompactionTriggerTokens(out.OpenAI.CompactionTrigger)
		if out.OpenAI.WebSearch == nil {
			out.OpenAI.WebSearch = defaultWebSearchConfig("openai")
		}
		out.OpenAI.ApiKey = ""
	}
	if out.Anthropic != nil {
		if out.Anthropic.CompactionEnabled == nil {
			enabled := true
			out.Anthropic.CompactionEnabled = &enabled
		}
		if out.Anthropic.CompactionInstruction == nil {
			out.Anthropic.CompactionInstruction = strptr(defaultBedrockCompactionInstruction)
		} else {
			v := normalizeCompactionInstructionText(*out.Anthropic.CompactionInstruction)
			out.Anthropic.CompactionInstruction = &v
		}
		out.Anthropic.CompactionTrigger = normalizeCompactionTriggerTokens(out.Anthropic.CompactionTrigger)
		if out.Anthropic.WebSearch == nil {
			out.Anthropic.WebSearch = defaultWebSearchConfig("anthropic")
		}
		out.Anthropic.ApiKey = ""
	}
	if out.Gemini != nil {
		out.Gemini.ApiKey = ""
	}
	if out.Bedrock != nil {
		if out.Bedrock.CompactionEnabled == nil {
			enabled := true
			out.Bedrock.CompactionEnabled = &enabled
		}
		if out.Bedrock.CompactionInstruction == nil {
			out.Bedrock.CompactionInstruction = strptr(defaultBedrockCompactionInstruction)
		} else {
			v := normalizeCompactionInstructionText(*out.Bedrock.CompactionInstruction)
			out.Bedrock.CompactionInstruction = &v
		}
		out.Bedrock.CompactionTrigger = normalizeCompactionTriggerTokens(out.Bedrock.CompactionTrigger)
		out.Bedrock.WebSearch = nil
		out.Bedrock.CodeExecution = nil
		out.Bedrock.ApiKey = ""
		out.Bedrock.AccessKeyID = ""
		out.Bedrock.SecretAccessKey = ""
		out.Bedrock.SessionToken = nil
		if out.Bedrock.Extra != nil {
			for i := range out.Bedrock.Extra {
				if out.Bedrock.Extra[i].IsSecret {
					out.Bedrock.Extra[i].Value = ""
				}
			}
		}
	}
	if out.GoogleAccessToken != nil {
		empty := ""
		out.GoogleAccessToken = &empty
	}
	if out.DatabricksHost != nil {
		v := strings.TrimSpace(*out.DatabricksHost)
		if v == "" {
			out.DatabricksHost = nil
		} else {
			out.DatabricksHost = &v
		}
	}
	if out.DatabricksWarehouseID != nil {
		v := strings.TrimSpace(*out.DatabricksWarehouseID)
		if v == "" {
			out.DatabricksWarehouseID = nil
		} else {
			out.DatabricksWarehouseID = &v
		}
	}
	if out.DatabricksOAuthClientID != nil {
		v := strings.TrimSpace(*out.DatabricksOAuthClientID)
		if v == "" {
			out.DatabricksOAuthClientID = nil
		} else {
			out.DatabricksOAuthClientID = &v
		}
	}
	if out.GitHubOAuthClientID != nil {
		v := strings.TrimSpace(*out.GitHubOAuthClientID)
		if v == "" {
			out.GitHubOAuthClientID = nil
		} else {
			out.GitHubOAuthClientID = &v
		}
	}
	if out.SlackOAuthClientID != nil {
		v := strings.TrimSpace(*out.SlackOAuthClientID)
		if v == "" {
			out.SlackOAuthClientID = nil
		} else {
			out.SlackOAuthClientID = &v
		}
	}
	if out.TrinoHost != nil {
		v := strings.TrimSpace(*out.TrinoHost)
		if v == "" {
			out.TrinoHost = nil
		} else {
			out.TrinoHost = &v
		}
	}
	if out.TrinoCatalog != nil {
		v := strings.TrimSpace(*out.TrinoCatalog)
		if v == "" {
			out.TrinoCatalog = nil
		} else {
			out.TrinoCatalog = &v
		}
	}
	if out.TrinoSchema != nil {
		v := strings.TrimSpace(*out.TrinoSchema)
		if v == "" {
			out.TrinoSchema = nil
		} else {
			out.TrinoSchema = &v
		}
	}
	if out.TrinoUser != nil {
		v := strings.TrimSpace(*out.TrinoUser)
		if v == "" {
			out.TrinoUser = nil
		} else {
			out.TrinoUser = &v
		}
	}
	if out.TrinoSource != nil {
		v := strings.TrimSpace(*out.TrinoSource)
		if v == "" {
			out.TrinoSource = nil
		} else {
			out.TrinoSource = &v
		}
	}
	if out.SnowflakeAccountIdentifier != nil {
		v := strings.TrimSpace(*out.SnowflakeAccountIdentifier)
		if v == "" {
			out.SnowflakeAccountIdentifier = nil
		} else {
			out.SnowflakeAccountIdentifier = &v
		}
	}
	if out.SnowflakeWarehouse != nil {
		v := strings.TrimSpace(*out.SnowflakeWarehouse)
		if v == "" {
			out.SnowflakeWarehouse = nil
		} else {
			out.SnowflakeWarehouse = &v
		}
	}
	if out.SnowflakeDatabase != nil {
		v := strings.TrimSpace(*out.SnowflakeDatabase)
		if v == "" {
			out.SnowflakeDatabase = nil
		} else {
			out.SnowflakeDatabase = &v
		}
	}
	if out.SnowflakeSchema != nil {
		v := strings.TrimSpace(*out.SnowflakeSchema)
		if v == "" {
			out.SnowflakeSchema = nil
		} else {
			out.SnowflakeSchema = &v
		}
	}
	if out.SnowflakeRole != nil {
		v := strings.TrimSpace(*out.SnowflakeRole)
		if v == "" {
			out.SnowflakeRole = nil
		} else {
			out.SnowflakeRole = &v
		}
	}
	if out.SnowflakeUser != nil {
		v := strings.TrimSpace(*out.SnowflakeUser)
		if v == "" {
			out.SnowflakeUser = nil
		} else {
			out.SnowflakeUser = &v
		}
	}
	if out.DatabaseQueryRowLimit == nil {
		queryLimit := DefaultDatabaseQueryRowLimit
		out.DatabaseQueryRowLimit = &queryLimit
	} else {
		queryLimit := *out.DatabaseQueryRowLimit
		if queryLimit < 1 {
			queryLimit = DefaultDatabaseQueryRowLimit
		}
		if queryLimit > MaxDatabaseQueryRowLimit {
			queryLimit = MaxDatabaseQueryRowLimit
		}
		out.DatabaseQueryRowLimit = &queryLimit
	}
	out.DatabaseQueryPolicy = sanitizeDatabaseQueryPolicy(out.DatabaseQueryPolicy)
	if legacyQueryPolicyMissing && out.DatabaseQueryRowLimit != nil {
		out.DatabaseQueryPolicy.MaxResultRows = *out.DatabaseQueryRowLimit
	}
	// databaseQueryRowLimit is a legacy migration input only. Keep its
	// serialized mirror synchronized for older clients, but make the policy
	// value authoritative so runtime consumers cannot observe two limits.
	queryLimit := out.DatabaseQueryPolicy.MaxResultRows
	out.DatabaseQueryRowLimit = &queryLimit

	for i := range out.ProviderConfigs {
		cfg := out.ProviderConfigs[i]
		providerID := strings.TrimSpace(cfg.ProviderID)
		// capabilityControls is the durable boundary between legacy provider
		// configs and configs created by the capability-aware editor. Legacy
		// configs keep their historical effective defaults; new configs must not
		// acquire consent-gated hosted tools merely because their legacy mirror is
		// absent.
		hasCapabilityControls := cfg.CapabilityControls != nil
		if providerID == "anthropic" {
			if cfg.CompactionEnabled == nil {
				enabled := true
				cfg.CompactionEnabled = &enabled
			}
			if cfg.CompactionInstruction == nil {
				cfg.CompactionInstruction = strptr(defaultBedrockCompactionInstruction)
			} else {
				v := normalizeCompactionInstructionText(*cfg.CompactionInstruction)
				cfg.CompactionInstruction = &v
			}
			cfg.CompactionTrigger = normalizeCompactionTriggerTokens(cfg.CompactionTrigger)
		}
		if providerID == "openai" {
			if cfg.CompactionEnabled == nil {
				enabled := true
				cfg.CompactionEnabled = &enabled
			}
			cfg.CompactionTrigger = normalizeOpenAICompactionTriggerTokens(cfg.CompactionTrigger)
			cfg.CompactionInstruction = nil
			if cfg.WebSearch == nil && !hasCapabilityControls {
				cfg.WebSearch = defaultWebSearchConfig("openai")
			}
		}
		if providerID == "compatible" {
			apiType := "chat_completions"
			if cfg.ApiType != nil && strings.TrimSpace(*cfg.ApiType) != "" {
				apiType = strings.TrimSpace(*cfg.ApiType)
			}
			if apiType == "responses" && cfg.CompactionEnabled != nil && *cfg.CompactionEnabled {
				cfg.CompactionTrigger = normalizeOpenAICompactionTriggerTokens(cfg.CompactionTrigger)
			} else {
				disabled := false
				cfg.CompactionEnabled = &disabled
				cfg.CompactionTrigger = nil
			}
			cfg.CompactionInstruction = nil
		}
		if providerID == "anthropic" && cfg.WebSearch == nil && !hasCapabilityControls {
			cfg.WebSearch = defaultWebSearchConfig("anthropic")
		}
		if providerID == "bedrock" {
			cfg.Model = normalizeBedrockModelID(cfg.Model)
			apiType := "messages"
			if cfg.ApiType != nil && strings.TrimSpace(*cfg.ApiType) != "" {
				apiType = strings.TrimSpace(*cfg.ApiType)
			}
			cfg.ApiType = &apiType
			if apiType == "messages" {
				if cfg.CompactionEnabled == nil {
					enabled := true
					cfg.CompactionEnabled = &enabled
				}
				if cfg.CompactionInstruction == nil {
					cfg.CompactionInstruction = strptr(defaultBedrockCompactionInstruction)
				} else {
					v := normalizeCompactionInstructionText(*cfg.CompactionInstruction)
					cfg.CompactionInstruction = &v
				}
				cfg.CompactionTrigger = normalizeCompactionTriggerTokens(cfg.CompactionTrigger)
			} else {
				disabled := false
				cfg.CompactionEnabled = &disabled
				cfg.CompactionInstruction = nil
				cfg.CompactionTrigger = nil
			}
			cfg.WebSearch = nil
			cfg.CodeExecution = nil
		}
		if cfg.ExtraHeaders != nil {
			for j := range cfg.ExtraHeaders {
				if cfg.ExtraHeaders[j].IsSecret {
					cfg.ExtraHeaders[j].Value = ""
				}
			}
		}
		cfg = normalizeProviderCapabilityMetadata(cfg)
		out.ProviderConfigs[i] = cfg
	}
	out.ProviderConfigs = reconcileGlobalWebSearchProviderConfigs(out.ProviderConfigs)
	var globalWebSearchEnabled *bool
	for _, cfg := range out.ProviderConfigs {
		if !providerSupportsHostedConnections(cfg) {
			continue
		}
		enabled := providerHostedConnectionEnabled(cfg, "hosted_web_search", cfg.WebSearch)
		globalWebSearchEnabled = &enabled
		break
	}
	canonicalizeLegacyWebSearchMirrors(&out, globalWebSearchEnabled)
	out.Connections = ensureEnabledConnectionsVisible(out)

	// Compatible profile secrets are stored in the encrypted secrets store, not settings.json.
	for i := range out.CompatibleProfiles {
		p := out.CompatibleProfiles[i]
		if p.ApiKey != nil {
			empty := ""
			p.ApiKey = &empty
		}
		// Secret header values are stored separately.
		if p.Extra != nil {
			for j := range p.Extra {
				if p.Extra[j].IsSecret {
					p.Extra[j].Value = ""
				}
			}
		}
		out.CompatibleProfiles[i] = p
	}

	return out
}

func normalizeBedrockModelID(raw string) string {
	model := strings.TrimSpace(raw)
	// This was the connection editor's old Bedrock default, but it is a direct
	// Anthropic API model name rather than a Bedrock Runtime model ID.
	if model == "claude-opus-4-6" {
		return "anthropic.claude-opus-4-6-v1"
	}
	return model
}

func validateSettings(in AppSettings) error {
	p := strings.TrimSpace(in.Provider.ProviderID)
	switch p {
	case "openai", "anthropic", "gemini", "compatible", "bedrock":
	default:
		return errors.New("provider.providerId must be one of: openai, anthropic, gemini, compatible, bedrock")
	}
	if p == "bedrock" {
		if in.Bedrock == nil || strings.TrimSpace(in.Bedrock.Region) == "" {
			return errors.New("bedrock.region is required when provider.providerId=bedrock")
		}
	}
	if len(in.CompatibleProfiles) == 0 {
		return errors.New("compatibleProfiles must contain at least one profile")
	}
	for _, prof := range in.CompatibleProfiles {
		if strings.TrimSpace(prof.ID) == "" {
			return errors.New("compatibleProfiles[].id is required")
		}
		if strings.TrimSpace(prof.DisplayName) == "" {
			return errors.New("compatibleProfiles[].displayName is required")
		}
		if strings.TrimSpace(prof.BaseURL) == "" {
			return errors.New("compatibleProfiles[].baseUrl is required")
		}
		if strings.TrimSpace(prof.Auth.Type) == "" || strings.TrimSpace(prof.Auth.Header) == "" {
			return errors.New("compatibleProfiles[].auth.type and auth.header are required")
		}
	}

	if len(in.ProviderConfigs) > 0 {
		ids := map[string]struct{}{}
		for _, cfg := range in.ProviderConfigs {
			id := strings.TrimSpace(cfg.ID)
			if id == "" {
				return errors.New("providerConfigs[].id is required")
			}
			if _, exists := ids[id]; exists {
				return errors.New("providerConfigs[].id must be unique")
			}
			ids[id] = struct{}{}

			pid := strings.TrimSpace(cfg.ProviderID)
			switch pid {
			case "openai", "anthropic", "gemini", "compatible", "bedrock":
			default:
				return errors.New("providerConfigs[].providerId must be one of: openai, anthropic, gemini, compatible, bedrock")
			}
		}

		if in.ActiveProviderConfigID != nil && strings.TrimSpace(*in.ActiveProviderConfigID) != "" {
			if _, ok := ids[strings.TrimSpace(*in.ActiveProviderConfigID)]; !ok {
				return errors.New("activeProviderConfigId must reference providerConfigs[].id")
			}
		}
	}
	if in.Atlassian != nil {
		jiraEnabled := in.Atlassian.Jira != nil && in.Atlassian.Jira.Enabled != nil && *in.Atlassian.Jira.Enabled
		confluenceEnabled := in.Atlassian.Confluence != nil && in.Atlassian.Confluence.Enabled != nil && *in.Atlassian.Confluence.Enabled
		if jiraEnabled || confluenceEnabled {
			if strings.TrimSpace(ptrStringValue(in.Atlassian.JiraBaseURL)) == "" {
				return errors.New("atlassian.jiraBaseUrl is required when Jira or Confluence tools are enabled")
			}
			if _, err := parseHTTPURL(ptrStringValue(in.Atlassian.JiraBaseURL)); err != nil {
				return errors.New("atlassian.jiraBaseUrl must be a valid http(s) URL")
			}
			if confluenceEnabled {
				if strings.TrimSpace(ptrStringValue(in.Atlassian.ConfluenceBaseURL)) == "" {
					return errors.New("atlassian.confluenceBaseUrl is required when Confluence tools are enabled")
				}
				if _, err := parseHTTPURL(ptrStringValue(in.Atlassian.ConfluenceBaseURL)); err != nil {
					return errors.New("atlassian.confluenceBaseUrl must be a valid http(s) URL")
				}
			}
		}
	}
	if in.SharePoint != nil && in.SharePoint.Enabled != nil && *in.SharePoint.Enabled {
		if strings.TrimSpace(ptrStringValue(in.SharePoint.SiteURL)) == "" {
			return errors.New("sharepoint.siteUrl is required when SharePoint tools are enabled")
		}
		if _, err := parseHTTPURL(ptrStringValue(in.SharePoint.SiteURL)); err != nil {
			return errors.New("sharepoint.siteUrl must be a valid http(s) URL")
		}
	}
	if in.ExternalAPI != nil {
		ids := map[string]struct{}{}
		for _, key := range in.ExternalAPI.APIKeys {
			id := strings.TrimSpace(key.ID)
			if id == "" {
				return errors.New("externalApi.apiKeys[].id is required")
			}
			if _, exists := ids[id]; exists {
				return errors.New("externalApi.apiKeys[].id must be unique")
			}
			ids[id] = struct{}{}
			if strings.TrimSpace(key.Hash) == "" {
				return errors.New("externalApi.apiKeys[].hash is required")
			}
		}
	}
	return nil
}

func ptrStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("invalid scheme")
	}
	if strings.TrimSpace(u.Host) == "" {
		return nil, errors.New("missing host")
	}
	return u, nil
}

type Store struct {
	mu       sync.RWMutex
	path     string
	settings AppSettings
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := &Store{
		path:     filepath.Join(dir, "settings.json"),
		settings: DefaultSettings(),
	}
	_ = st.Load() // best-effort; defaults if missing/invalid
	return st, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Get() AppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.Clone()
}

func (s *Store) Load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var next AppSettings
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	next = sanitizeSettings(next)
	if err := validateSettings(next); err != nil {
		return err
	}
	canonical, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	// Sanitization is a migration boundary, not merely an in-memory view. If
	// older builds left obsolete provider-hosted tool fields on disk, rewrite
	// the file before exposing settings to the rest of the process. Otherwise a
	// later launch (or an older client) can read and re-save the stale schema.
	if !bytes.Equal(bytes.TrimSpace(b), bytes.TrimSpace(canonical)) {
		if err := atomicfile.WriteFile(s.path, canonical, 0o600); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	return nil
}

func (s *Store) Save(next AppSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(next)
}

// saveLocked persists next while s.mu is held by the caller.
func (s *Store) saveLocked(next AppSettings) error {
	next = sanitizeSettings(next)
	if err := validateSettings(next); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.path, b, 0o600); err != nil {
		return err
	}
	s.settings = next
	return nil
}
