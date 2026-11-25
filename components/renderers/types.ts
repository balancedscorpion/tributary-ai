import type { AppSettings, Artifact } from '@/types';

export interface RendererProps {
  artifact: Artifact;
  toolResult?: unknown;
  settings?: AppSettings;
  className?: string;
}

export interface RendererEntry {
  component: React.ComponentType<RendererProps>;
  label: string;
  icon?: React.ComponentType<{ size?: number }>;
  /**
   * When true, the renderer takes over the full artifact-panel body. The
   * panel will skip its own QueryMetadata tabs (Data & Charts / Schema /
   * Query Info) and render this component directly. Connector-specific
   * inspectors (Query, GitHub, …) set this so they don't get wrapped in
   * an extra layer of chrome.
   */
  ownsChrome?: boolean;
}
