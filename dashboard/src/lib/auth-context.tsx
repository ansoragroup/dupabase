"use client";

import { deferEffect } from "@/lib/defer-effect";

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  useRef,
  type ReactNode,
} from "react";
import { platformAuth, type PlatformUser } from "./api";

function clearSQLHistory() {
  localStorage.removeItem("dupabase_sql_history");
  for (let i = sessionStorage.length - 1; i >= 0; i--) {
    const key = sessionStorage.key(i);
    if (key?.startsWith("dupabase_sql_history:")) sessionStorage.removeItem(key);
  }
}

interface AuthState {
  user: PlatformUser | null;
  token: string | null;
  loading: boolean;
}

interface AuthContextType extends AuthState {
  login: (email: string, password: string) => Promise<string | null>;
  register: (email: string, password: string, inviteCode?: string) => Promise<string | null>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextType | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const sessionVersion = useRef(0);
  const [state, setState] = useState<AuthState>({
    user: null,
    token: null,
    loading: true,
  });

  const loadUser = useCallback(async (token: string) => {
    const version = ++sessionVersion.current;
    const { data, error } = await platformAuth.me(token);
    if (version !== sessionVersion.current || localStorage.getItem("platform_token") !== token) return;
    if (error || !data) {
      localStorage.removeItem("platform_token");
      clearSQLHistory();
      setState({ user: null, token: null, loading: false });
      return;
    }
    setState({ user: data, token, loading: false });
  }, []);

  useEffect(() => deferEffect(() => {
    const token = localStorage.getItem("platform_token");
    if (token) {
      loadUser(token);
    } else {
      setState((s) => ({ ...s, loading: false }));
    }
  }), [loadUser]);

  useEffect(() => {
    const syncSession = (event: StorageEvent) => {
      if (event.key !== "platform_token" && event.key !== null) return;
      ++sessionVersion.current;
      clearSQLHistory();
      const token = localStorage.getItem("platform_token");
      setState({ user: null, token, loading: !!token });
      if (token) void loadUser(token);
    };
    window.addEventListener("storage", syncSession);
    return () => window.removeEventListener("storage", syncSession);
  }, [loadUser]);

  const login = async (email: string, password: string) => {
    const version = ++sessionVersion.current;
    const { data, error } = await platformAuth.login(email, password);
    if (version !== sessionVersion.current) return "Session changed";
    if (error || !data) return error || "Login failed";
    clearSQLHistory();
    localStorage.setItem("platform_token", data.token);
    setState({ user: data.user, token: data.token, loading: false });
    return null;
  };

  const register = async (email: string, password: string, inviteCode?: string) => {
    const version = ++sessionVersion.current;
    const { data, error } = await platformAuth.register(email, password, inviteCode);
    if (version !== sessionVersion.current) return "Session changed";
    if (error || !data) return error || "Registration failed";
    clearSQLHistory();
    localStorage.setItem("platform_token", data.token);
    setState({ user: data.user, token: data.token, loading: false });
    return null;
  };

  const logout = () => {
    ++sessionVersion.current;
    localStorage.removeItem("platform_token");
    clearSQLHistory();
    setState({ user: null, token: null, loading: false });
  };

  return (
    <AuthContext.Provider value={{ ...state, login, register, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
