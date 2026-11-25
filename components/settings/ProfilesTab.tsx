import React, { useState } from 'react';
import { Plus, SaveAll, X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Card } from '@/components/ui/card';
import type { AppSettings, SavedConfig } from '@/types';

interface ProfilesTabProps {
  profiles: SavedConfig[];
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  onSaveProfile: (name: string, settings: AppSettings) => void;
  onDeleteProfile: (id: string) => void;
}

const ProfilesTab: React.FC<ProfilesTabProps> = ({
  profiles, localSettings, setLocalSettings, onSaveProfile, onDeleteProfile,
}) => {
  const [showInput, setShowInput] = useState(false);
  const [newName, setNewName] = useState('');

  const handleSave = () => {
    if (!newName.trim()) return;
    onSaveProfile(newName, localSettings);
    setNewName('');
    setShowInput(false);
  };

  const handleLoad = (profile: SavedConfig) => {
    if (window.confirm(`Load settings from "${profile.name}"? Unsaved changes will be lost.`)) {
      setLocalSettings(profile.settings);
    }
  };

  const handleDelete = (id: string) => {
    if (!window.confirm('Delete this profile?')) return;
    onDeleteProfile(id);
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-muted-foreground">Saved Profiles</h3>
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => setShowInput(!showInput)}>
          <Plus size={14} /> New Profile
        </Button>
      </div>

      {showInput && (
        <div className="flex items-center gap-2 animate-fade-in">
          <Input
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder="Profile Name (e.g. OpenAI Prod)"
            autoFocus
          />
          <Button size="sm" disabled={!newName.trim()} onClick={handleSave}>
            <SaveAll size={16} />
          </Button>
        </div>
      )}

      {profiles.length > 0 ? (
        <div className="flex flex-wrap gap-2">
          {profiles.map(profile => (
            <Card key={profile.id} className="group flex items-center gap-2 px-3 py-1.5 hover:border-primary/30 transition-colors">
              <button
                onClick={() => handleLoad(profile)}
                className="text-sm text-card-foreground hover:text-foreground truncate max-w-[160px]"
              >
                {profile.name}
              </button>
              <button
                onClick={() => handleDelete(profile.id)}
                className="text-muted-foreground hover:text-destructive opacity-0 group-hover:opacity-100 transition-opacity"
              >
                <X size={14} />
              </button>
            </Card>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground italic">
          No saved profiles yet. Save configurations to quickly switch environments.
        </p>
      )}
    </div>
  );
};

export default ProfilesTab;
