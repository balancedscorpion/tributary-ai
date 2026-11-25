'use client';

import React, { useState } from 'react';
import { KeyRound } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';

interface SecretFieldProps {
  label: string;
  placeholder?: string;
  isSet: boolean;
  onSet: (value: string) => void | Promise<void>;
  onClear: () => void | Promise<void>;
  optional?: boolean;
}

const redact = (key: string): string => {
  const t = key.trim();
  if (t.length <= 6) return '••••••';
  return `${t.slice(0, 6)}••••••${t.slice(-2)}`;
};

const SecretField: React.FC<SecretFieldProps> = ({
  label,
  placeholder,
  isSet,
  onSet,
  onClear,
  optional,
}) => {
  const [draft, setDraft] = useState('');
  const [editing, setEditing] = useState(false);

  if (isSet && !editing) {
    return (
      <div className="space-y-1.5">
        <Label className="text-xs flex items-center gap-2">
          {label}
          {optional && <span className="text-muted-foreground italic text-[10px]">optional</span>}
        </Label>
        <div className="flex items-center justify-between gap-2 px-3 py-2 rounded-md border border-border bg-muted/30">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <KeyRound size={13} />
            <span className="font-mono text-[11px] tracking-wider">••••••••</span>
            <span className="text-[9px] font-semibold text-emerald-500 bg-emerald-500/10 px-1.5 py-0.5 rounded">
              Set
            </span>
          </div>
          <div className="flex items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              className="h-6 px-2 text-[11px]"
              onClick={() => {
                setEditing(true);
                setDraft('');
              }}
            >
              Change
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="h-6 px-2 text-[11px] text-destructive hover:text-destructive"
              onClick={() => void onClear()}
            >
              Clear
            </Button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <Label className="text-xs flex items-center gap-2">
        {label}
        {optional && <span className="text-muted-foreground italic text-[10px]">optional</span>}
      </Label>
      <div className="flex items-center gap-2">
        <Input
          type="password"
          placeholder={placeholder}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          className="flex-1 font-mono"
        />
        <Button
          size="sm"
          className="h-8 px-3 text-xs"
          disabled={!draft.trim()}
          onClick={() => {
            void onSet(draft);
            setDraft('');
            setEditing(false);
          }}
        >
          Set
        </Button>
        {editing && (
          <Button
            variant="outline"
            size="sm"
            className="h-8 px-3 text-xs"
            onClick={() => setEditing(false)}
          >
            Cancel
          </Button>
        )}
      </div>
    </div>
  );
};

export default SecretField;
