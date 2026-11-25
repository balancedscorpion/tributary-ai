import React from 'react';
import type { ChatImageAttachment, ConnectionCatalogId } from '@/types';

/**
 * Chat composer actions made available to deeply nested surfaces (settings
 * sheets, the artifact inspector, tool renderers) without prop drilling.
 *
 * All members are optional: a consumer must hide or disable its affordance
 * when the corresponding handler is absent rather than assume availability.
 * App.tsx provides the same handlers it already passes to SettingsDialog.
 */
export interface ChatActions {
  attachFile?: (file: File) => void | Promise<void>;
  insertDraftText?: (text: string) => void;
  /** Open Settings on the Connections tab, optionally focused on one connection. */
  openConnections?: (id?: ConnectionCatalogId) => void;
  /**
   * A rendered tool result carried an auth-class error for this connection.
   * The app re-verifies the credentials strictly and surfaces a reconnect
   * prompt when confirmed. `dedupeKey` (tool_call_id) prevents re-fires when
   * the transcript re-renders.
   */
  reportConnectionAuthFailure?: (id: ConnectionCatalogId, dedupeKey: string) => void;
  /** Attach an image to the composer (e.g. a generated image as an edit source). */
  attachImage?: (image: ChatImageAttachment) => void;
  /** Open the Image Generation connection settings. */
  openImageSettings?: () => void;
}

export const ChatActionsContext: React.Context<ChatActions> = React.createContext<ChatActions>({});

export function useChatActions(): ChatActions {
  return React.useContext(ChatActionsContext);
}
