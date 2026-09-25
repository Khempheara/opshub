import { Database, Globe, Network, Server, type LucideIcon } from 'lucide-react';
import type { AssetKind } from '@/lib/api/generated/model';

export const kindIcon: Record<AssetKind, LucideIcon> = {
  server: Server,
  cluster: Network,
  database: Database,
  domain: Globe,
};
