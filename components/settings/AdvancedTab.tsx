import React from 'react';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import {
  DEFAULT_DATABASE_QUERY_POLICY,
  DEFAULT_DATABASE_QUERY_ROW_LIMIT,
  MAX_DATABASE_QUERY_ROW_LIMIT,
} from '@/constants';
import type { NormalizedActivationStatus } from '@/services/activationApi';
import type { AppSettings } from '@/types';
import LicensingSection from './LicensingSection';

interface AdvancedTabProps {
  active: boolean;
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  onActivationStatus?: (status: NormalizedActivationStatus | null) => void;
}

const AdvancedTab: React.FC<AdvancedTabProps> = ({
  active,
  localSettings,
  setLocalSettings,
  onActivationStatus,
}) => {
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <Label className="text-xs mb-1.5 block">Max Tool Iterations</Label>
          <Input
            type="number"
            min={1}
            max={200}
            value={localSettings.maxToolIterations ?? 200}
            onChange={(e) => {
              const raw = Number(e.target.value);
              const next = Number.isFinite(raw) ? Math.max(1, Math.min(200, Math.trunc(raw))) : 200;
              setLocalSettings(prev => ({ ...prev, maxToolIterations: next }));
            }}
            className="font-mono"
          />
          <p className="text-[10px] text-muted-foreground mt-1">
            Safety limit for the agent loop. Increase for deep exploration; lower to prevent runaway tool usage.
          </p>
        </div>
        <div>
          <Label className="text-xs mb-1.5 block">Remote DB Row Limit</Label>
          <Input
            type="number"
            min={1}
            max={MAX_DATABASE_QUERY_ROW_LIMIT}
            value={
              localSettings.databaseQueryPolicy?.maxResultRows
              ?? localSettings.databaseQueryRowLimit
              ?? DEFAULT_DATABASE_QUERY_ROW_LIMIT
            }
            onChange={(e) => {
              const raw = Number(e.target.value);
              const next = Number.isFinite(raw)
                ? Math.max(1, Math.min(MAX_DATABASE_QUERY_ROW_LIMIT, Math.trunc(raw)))
                : DEFAULT_DATABASE_QUERY_ROW_LIMIT;
              setLocalSettings(prev => ({
                ...prev,
                // Keep the legacy field mirrored for older servers while the
                // long-horizon policy remains the authoritative setting.
                databaseQueryRowLimit: next,
                databaseQueryPolicy: {
                  ...(prev.databaseQueryPolicy ?? DEFAULT_DATABASE_QUERY_POLICY),
                  maxResultRows: next,
                },
              }));
            }}
            className="font-mono"
          />
          <p className="text-[10px] text-muted-foreground mt-1">
            Max rows returned per query page for BigQuery, Databricks, Trino, and Snowflake calls. Use pagination to fetch additional rows.
          </p>
        </div>
        <div className="flex items-center gap-3 pt-6">
          <Switch
            checked={localSettings.debug ?? false}
            onCheckedChange={(checked) => setLocalSettings(prev => ({ ...prev, debug: checked }))}
          />
          <div>
            <Label className="text-xs">Debug Mode</Label>
            <p className="text-[10px] text-muted-foreground">Log non-sensitive debug info to console.</p>
          </div>
        </div>
      </div>

      <div className="pt-2">
        <LicensingSection active={active} onStatus={onActivationStatus} />
      </div>
    </div>
  );
};

export default AdvancedTab;
