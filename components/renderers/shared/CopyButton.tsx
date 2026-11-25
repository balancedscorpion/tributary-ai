import React, { useState } from 'react';
import { Copy, Check } from 'lucide-react';
import { Button } from '@/components/ui/button';

interface CopyButtonProps {
  text: string;
  className?: string;
}

const CopyButton: React.FC<CopyButtonProps> = ({ text, className }) => {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // ignore
    }
  };

  return (
    <Button
      variant="ghost"
      size="sm"
      className={`h-6 px-2 text-[10px] gap-1.5 ${className ?? ''}`}
      onClick={handleCopy}
    >
      {copied ? <Check size={12} className="text-accent" /> : <Copy size={12} />}
      {copied ? 'Copied!' : 'Copy'}
    </Button>
  );
};

export default CopyButton;
