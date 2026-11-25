import React from 'react';
import UsageTab from '@/components/settings/UsageTab';

/** Isolated browser fixture for the read-only Settings usage report. */
const UsageFixture: React.FC = () => (
  <main className="min-h-screen bg-background p-6 text-foreground">
    <div className="mx-auto max-w-5xl rounded-xl border bg-card p-5 shadow-sm">
      <UsageTab active />
    </div>
  </main>
);

export default UsageFixture;
