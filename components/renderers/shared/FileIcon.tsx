import React from 'react';
import {
  File, FileCode, FileText, Braces, Database, Table, Globe,
} from 'lucide-react';

const EXT_MAP: Record<string, React.ComponentType<{ size?: number; className?: string }>> = {
  ts: FileCode, tsx: FileCode, js: FileCode, jsx: FileCode,
  py: FileCode, go: FileCode, rs: FileCode, rb: FileCode,
  java: FileCode, c: FileCode, cpp: FileCode, h: FileCode,
  json: Braces, jsonl: Braces,
  sql: Database,
  csv: Table, tsv: Table, parquet: Table,
  md: FileText, txt: FileText, yaml: FileText, yml: FileText, toml: FileText,
  html: Globe, css: Globe, scss: Globe,
};

interface FileIconProps {
  filename: string;
  size?: number;
  className?: string;
}

const FileIcon: React.FC<FileIconProps> = ({ filename, size = 16, className }) => {
  const ext = filename.match(/\.([a-zA-Z0-9]+)$/)?.[1]?.toLowerCase() ?? '';
  const Icon = EXT_MAP[ext] || File;
  return <Icon size={size} className={className} />;
};

export default FileIcon;
