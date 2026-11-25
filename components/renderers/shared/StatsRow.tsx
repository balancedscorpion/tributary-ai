import React from 'react';

interface StatsRowProps {
  children: React.ReactNode;
  className?: string;
}

const StatsRow: React.FC<StatsRowProps> = ({ children, className }) => {
  return (
    <div className={`flex flex-wrap gap-2 ${className ?? ''}`}>
      {children}
    </div>
  );
};

export default StatsRow;
