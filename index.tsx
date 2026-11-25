import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import AuditTrailFixture from './components/dev/AuditTrailFixture';
import DashboardArtifactProposals from './components/dev/DashboardArtifactProposals';
import ApprovalsFixture from './components/dev/ApprovalsFixture';
import FilesAuditFixture from './components/dev/FilesAuditFixture';
import SourcesFixture from './components/dev/SourcesFixture';
import UsageFixture from './components/dev/UsageFixture';
import ImageGenerationFixture from './components/dev/ImageGenerationFixture';
import './globals.css';
import 'katex/dist/katex.min.css';
import { installDesktopFetchAuth } from './services/apiClient';
import { applyUiTextZoom, readStoredUiTextZoom } from './lib/uiTextZoom';

installDesktopFetchAuth();
applyUiTextZoom(readStoredUiTextZoom());

const rootElement = document.getElementById('root');
if (!rootElement) {
  throw new Error("Could not find root element to mount to");
}

const root = ReactDOM.createRoot(rootElement);
const RootComponent =
  window.location.pathname === '/dev/audit-trail'
    ? AuditTrailFixture
    : window.location.pathname === '/dev/audit/dashboard'
      ? DashboardArtifactProposals
      : window.location.pathname === '/dev/approvals'
        ? ApprovalsFixture
        : window.location.pathname === '/dev/files-audit'
          ? FilesAuditFixture
          : window.location.pathname === '/dev/sources'
            ? SourcesFixture
            : window.location.pathname === '/dev/usage'
              ? UsageFixture
              : window.location.pathname === '/dev/image-generation'
                ? ImageGenerationFixture
                : App;

root.render(
  <React.StrictMode>
    <RootComponent />
  </React.StrictMode>
);
