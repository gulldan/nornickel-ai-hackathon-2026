import { useEffect, useMemo, useState } from "react";

import { adminListUsers, type ApiAccount } from "@/features/admin";
import { useAuth } from "@/features/auth";

export function useOwnerNames(): (ownerId: string | undefined) => string | null {
  const { roles } = useAuth();
  const isAdmin = roles.includes("admin");
  const [users, setUsers] = useState<ApiAccount[]>([]);

  useEffect(() => {
    if (!isAdmin) return;
    let cancelled = false;
    adminListUsers()
      .then((list) => {
        if (!cancelled) setUsers(list);
      })
      .catch(() => {
        if (!cancelled) setUsers([]);
      });
    return () => {
      cancelled = true;
    };
  }, [isAdmin]);

  const byId = useMemo(() => new Map(users.map((u) => [u.id, u.username])), [users]);

  return useMemo(
    () => (ownerId) => {
      if (!isAdmin || !ownerId) return null;
      return byId.get(ownerId) ?? ownerId.slice(0, 8);
    },
    [byId, isAdmin],
  );
}
