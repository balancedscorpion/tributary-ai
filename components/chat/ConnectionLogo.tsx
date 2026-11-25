import React from 'react';
import type { ConnectionMeta } from '@/lib/connectionsCatalog';

interface ConnectionLogoProps {
  meta: ConnectionMeta;
  /** Optional extra class on the <img>/<svg> element. */
  className?: string;
}

/**
 * Renders a connection's real brand mark from public/brand-logos, falling back
 * to its lucide icon if the asset cannot be loaded. Monochrome marks (GitHub)
 * are tinted light by the caller's `.ta-mono` wrapper class.
 */
const ConnectionLogo: React.FC<ConnectionLogoProps> = ({ meta, className }) => {
  const [failed, setFailed] = React.useState(false);
  if (failed) {
    const Icon = meta.icon;
    return <Icon className={className} aria-hidden="true" />;
  }
  return (
    <img
      src={meta.logo}
      alt=""
      className={className}
      draggable={false}
      onError={() => setFailed(true)}
    />
  );
};

export default ConnectionLogo;
