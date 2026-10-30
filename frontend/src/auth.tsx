import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { get, hasToken, post, setAuthLostHandler, setTokens } from './api';

export type Role = 'student' | 'instructor' | 'advisor' | 'dept_secretary' | 'registrar' | 'reporting';

export interface User {
  id: number;
  email: string;
  full_name: string;
  role: Role;
  student_id?: number;
}

export const ROLE_LABEL: Record<Role, string> = {
  student: 'Öğrenci',
  instructor: 'Öğretim üyesi',
  advisor: 'Danışman',
  dept_secretary: 'Bölüm sekreteri',
  registrar: 'Öğrenci işleri',
  reporting: 'Raporlama',
};

interface AuthCtx {
  user: User | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const Ctx = createContext<AuthCtx>(null as unknown as AuthCtx);
export const useAuth = () => useContext(Ctx);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(hasToken());

  useEffect(() => {
    setAuthLostHandler(() => setUser(null));
    if (!hasToken()) return;
    get<User>('/me')
      .then(setUser)
      .catch(() => setTokens(null, null))
      .finally(() => setLoading(false));
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    const t = await post<{ access_token: string; refresh_token: string }>('/auth/login', { email, password });
    setTokens(t.access_token, t.refresh_token);
    setUser(await get<User>('/me'));
  }, []);

  const logout = useCallback(async () => {
    const r = localStorage.getItem('refresh');
    try {
      if (r) await post('/auth/logout', { refresh_token: r });
    } catch {
      /* çıkış zaten yerel olarak yapılır */
    }
    setTokens(null, null);
    setUser(null);
  }, []);

  const value = useMemo(() => ({ user, loading, login, logout }), [user, loading, login, logout]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
