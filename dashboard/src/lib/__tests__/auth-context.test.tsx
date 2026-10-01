import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "../auth-context";
import { platformAuth, type PlatformUser } from "../api";

const fixture: PlatformUser = { id: "fixture", email: "fixture@example.test", pg_username: "u_fixture", display_name: null, is_admin: false, created_at: "2026-10-01T00:00:00Z" };
function SessionProbe() {
  const { user, loading, logout } = useAuth();
  return <><output data-testid="session">{loading ? "loading" : user?.id ?? "signed-out"}</output><button onClick={logout}>Logout</button></>;
}

function fixtureStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() { return values.size; },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key); },
    setItem: (key, value) => { values.set(String(key), String(value)); },
  };
}

beforeEach(() => {
  vi.stubGlobal("localStorage", fixtureStorage());
  vi.stubGlobal("sessionStorage", fixtureStorage());
  localStorage.clear(); sessionStorage.clear();
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("does not restore an old session when its pending user lookup completes after logout", async () => {
  let resolve!: (value: { data: PlatformUser; error: null }) => void;
  const me = vi.spyOn(platformAuth, "me").mockImplementation(() => new Promise(done => { resolve = done; }));
  localStorage.setItem("platform_token", "fixture-token");
  localStorage.setItem("dupabase_sql_history", "legacy-sensitive-query");
  sessionStorage.setItem("dupabase_sql_history:fixture:project", "sensitive-query");
  render(<AuthProvider><SessionProbe /></AuthProvider>);
  await waitFor(() => expect(me).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByText("Logout"));
  await act(async () => { resolve({ data: fixture, error: null }); });
  expect(screen.getByTestId("session")).toHaveTextContent("signed-out");
  expect(localStorage.getItem("platform_token")).toBeNull();
  expect(localStorage.getItem("dupabase_sql_history")).toBeNull();
  expect(sessionStorage.getItem("dupabase_sql_history:fixture:project")).toBeNull();
});

it("clears this tab's SQL history and user when another tab logs out", async () => {
  vi.spyOn(platformAuth, "me").mockResolvedValue({ data: fixture, error: null });
  localStorage.setItem("platform_token", "fixture-token");
  render(<AuthProvider><SessionProbe /></AuthProvider>);
  await waitFor(() => expect(screen.getByTestId("session")).toHaveTextContent("fixture"));
  sessionStorage.setItem("dupabase_sql_history:fixture:project", "sensitive-query");
  act(() => {
    localStorage.removeItem("platform_token");
    window.dispatchEvent(new StorageEvent("storage", { key: "platform_token", oldValue: "fixture-token", newValue: null }));
  });
  expect(screen.getByTestId("session")).toHaveTextContent("signed-out");
  expect(sessionStorage.getItem("dupabase_sql_history:fixture:project")).toBeNull();
});
