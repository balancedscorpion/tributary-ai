import React from 'react';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import type { AppSettings } from '@/types';

interface SystemPromptTabProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
}

const SystemPromptTab: React.FC<SystemPromptTabProps> = ({ localSettings, setLocalSettings }) => {
  return (
    <div className="space-y-3">
      <Label className="text-xs">System Prompt</Label>
      <p className="text-xs text-muted-foreground">
        Define the AI assistant's behavior and personality.
      </p>
      <Textarea
        value={localSettings.systemPrompt}
        onChange={(e) => setLocalSettings(prev => ({ ...prev, systemPrompt: e.target.value }))}
        rows={8}
        placeholder="You are a helpful assistant..."
        className="resize-none leading-relaxed font-mono text-sm"
      />
    </div>
  );
};

export default SystemPromptTab;
