import React from 'react';

interface StatsCardProps {
  icon?: React.ReactNode;
  label: string;
  value: string | number;
  className?: string;
}

const StatsCard: React.FC<StatsCardProps> = ({ icon, label, value, className }) => {
  return (
    <div className={`bg-card/50 border border-border rounded-lg px-3 py-2 flex items-center gap-2.5 min-w-0 ${className ?? ''}`}>
      {icon && <div className="text-muted-foreground shrink-0">{icon}</div>}
      <div className="min-w-0">
        <p className="text-[10px] text-muted-foreground uppercase tracking-wider font-semibold truncate">{label}</p>
        <p className="text-sm font-semibold text-foreground truncate">{String(value)}</p>
      </div>
    </div>
  );
};

export default StatsCard;
