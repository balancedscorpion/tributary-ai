import React from 'react';

const DataAnalysisIcon: React.FC<{ size?: number; className?: string }> = ({ size = 64, className }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 64 64"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    className={className}
  >
    {/* Bar chart bars */}
    <rect x="8" y="38" width="8" height="18" rx="1" fill="currentColor" opacity="0.25" />
    <rect x="20" y="28" width="8" height="28" rx="1" fill="currentColor" opacity="0.35" />
    <rect x="32" y="22" width="8" height="34" rx="1" fill="currentColor" opacity="0.45" />
    <rect x="44" y="14" width="8" height="42" rx="1" fill="currentColor" opacity="0.55" />
    {/* Trend line */}
    <polyline
      points="12,34 24,26 36,20 48,10"
      fill="none"
      strokeWidth="2.5"
      opacity="0.8"
    />
    {/* Trend line dots */}
    <circle cx="12" cy="34" r="2.5" fill="currentColor" opacity="0.8" />
    <circle cx="24" cy="26" r="2.5" fill="currentColor" opacity="0.8" />
    <circle cx="36" cy="20" r="2.5" fill="currentColor" opacity="0.8" />
    <circle cx="48" cy="10" r="2.5" fill="currentColor" opacity="0.8" />
    {/* Base line */}
    <line x1="4" y1="56" x2="58" y2="56" strokeWidth="2" opacity="0.4" />
  </svg>
);

const EmptyState: React.FC = () => {
  return (
    <div className="h-full flex flex-col items-center justify-center text-muted-foreground/50 select-none">
      <DataAnalysisIcon size={64} className="mb-4 text-muted-foreground/30" />
      <p className="text-lg font-medium">Start a conversation</p>
    </div>
  );
};

export default EmptyState;
