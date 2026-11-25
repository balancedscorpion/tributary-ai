import React, { useState, useMemo } from 'react';
import { Search } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';

export interface ItemListItem {
  icon?: React.ReactNode;
  label: string;
  description?: string;
}

interface ItemListProps {
  items: ItemListItem[];
  searchable?: boolean;
  title?: string;
  className?: string;
}

const ItemList: React.FC<ItemListProps> = ({ items, searchable = false, title, className }) => {
  const [filter, setFilter] = useState('');

  const filtered = useMemo(() => {
    if (!filter.trim()) return items;
    const q = filter.toLowerCase();
    return items.filter((i) => i.label.toLowerCase().includes(q) || i.description?.toLowerCase().includes(q));
  }, [items, filter]);

  return (
    <div className={`flex flex-col h-full ${className ?? ''}`}>
      {(title || searchable) && (
        <div className="flex items-center gap-2 px-3 py-2 border-b border-border">
          {title && <span className="text-xs font-semibold text-muted-foreground">{title}</span>}
          <Badge variant="secondary" className="text-[10px] px-1.5 py-0">{items.length}</Badge>
          {searchable && (
            <div className="relative ml-auto">
              <Search size={12} className="absolute left-2 top-1/2 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Filter..."
                className="h-6 pl-6 text-xs w-40"
              />
            </div>
          )}
        </div>
      )}
      <div className="flex-1 overflow-auto">
        {filtered.map((item, i) => (
          <div
            key={i}
            className="flex items-center gap-2.5 px-3 py-1.5 hover:bg-muted/30 transition-colors border-b border-border/30 last:border-b-0"
          >
            {item.icon && <span className="text-muted-foreground shrink-0">{item.icon}</span>}
            <div className="min-w-0 flex-1">
              <p className="text-xs font-mono text-foreground truncate">{item.label}</p>
              {item.description && (
                <p className="text-[10px] text-muted-foreground truncate">{item.description}</p>
              )}
            </div>
          </div>
        ))}
        {filtered.length === 0 && (
          <p className="text-xs text-muted-foreground text-center py-4">No items match the filter.</p>
        )}
      </div>
    </div>
  );
};

export default ItemList;
