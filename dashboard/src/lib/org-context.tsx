"use client";

import { deferEffect } from "@/lib/defer-effect";

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import { useAuth } from "./auth-context";
import { orgs as orgsApi, type Organization } from "./api";

interface OrgContextType {
  orgs: Organization[];
  activeOrg: Organization | null;
  setActiveOrg: (org: Organization) => void;
  refreshOrgs: () => Promise<void>;
  loading: boolean;
}

const OrgContext = createContext<OrgContextType | null>(null);

export function OrgProvider({ children }: { children: ReactNode }) {
  const { token } = useAuth();
  const [orgList, setOrgList] = useState<Organization[]>([]);
  const [activeOrg, setActiveOrgState] = useState<Organization | null>(null);
  const [loading, setLoading] = useState(true);

  const refreshOrgs = useCallback(async () => {
    if (!token) return;
    const { data } = await orgsApi.list(token);
    if (data) {
      setOrgList(data);
      const storedId = localStorage.getItem("active_org_id");
      const found = data.find((o) => o.id === storedId);
      if (found) {
        setActiveOrgState(found);
      } else if (data.length > 0) {
        setActiveOrgState(data[0]);
        localStorage.setItem("active_org_id", data[0].id);
      }
    }
    setLoading(false);
  }, [token]);

  useEffect(() => deferEffect(() => {
    refreshOrgs();
  }), [refreshOrgs]);

  const setActiveOrg = (org: Organization) => {
    setActiveOrgState(org);
    localStorage.setItem("active_org_id", org.id);
  };

  return (
    <OrgContext.Provider
      value={{ orgs: orgList, activeOrg, setActiveOrg, refreshOrgs, loading }}
    >
      {children}
    </OrgContext.Provider>
  );
}

export function useOrg() {
  const ctx = useContext(OrgContext);
  if (!ctx) throw new Error("useOrg must be used within OrgProvider");
  return ctx;
}
