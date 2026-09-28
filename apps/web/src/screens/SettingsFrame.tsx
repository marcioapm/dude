/**
 * The settings screens' shell: a left menu of pages — the roles as
 * sub-pages of Agents — and the page. The organization's and a project's
 * share it, and share the Agents and Delivery pages (settingsPages.tsx);
 * each adds its own (the organization's GitHub, a project's repositories).
 */

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { AgentAvatar, SettingsLayout, type SettingsNavItem } from "@dude/design-system/components";
import { Spinner, useToast } from "@dude/design-system/primitives";
import { SETTINGS_ROLE_LABEL, SETTINGS_ROLES, type SettingsPatch, type SettingsResponse, type SettingsRole } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { deliveryChanged, roleChanged } from "../settings.ts";
import type { SettingsScope } from "./settingsPages.tsx";

/** The settings, loaded and kept current: what the pages read, and how they save. */
export function useSettings(client: ApiClient, load: () => Promise<SettingsResponse>, save: (patch: SettingsPatch) => Promise<SettingsResponse>) {
  const [settings, setSettings] = useState<SettingsResponse | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const { toast } = useToast();
  // Only the latest answer may land: two saves in a row start two.
  const latest = useRef(0);
  const take = useCallback((next: SettingsResponse) => {
    latest.current++;
    setSettings(next);
  }, []);
  useEffect(() => {
    const mine = ++latest.current;
    load().then(
      (s) => mine === latest.current && setSettings(s),
      (err: unknown) => mine === latest.current && setProblem(errorText(err)),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client]);
  const scope: SettingsScope | null = settings
    ? {
        client,
        settings,
        replace: take,
        patch: async (patch, done) => {
          try {
            take(await save(patch));
            toast({ title: done, tone: "success" });
          } catch (err) {
            toast({ title: errorText(err), tone: "danger" });
            throw err;
          }
        },
      }
    : null;
  return { scope, problem };
}

export const isRole = (page: string): page is SettingsRole => (SETTINGS_ROLES as readonly string[]).includes(page);

/** Agents, with a sub-page per role; on a project, a mark on each role it changes. */
export function agentsNav(settings: SettingsResponse): SettingsNavItem {
  const project = Boolean(settings.project);
  const changed = SETTINGS_ROLES.filter((r) => roleChanged(settings.roles[r])).length;
  return {
    id: "agents",
    label: "Agents",
    icon: "agent",
    note: project && changed ? `${changed} changed` : undefined,
    items: SETTINGS_ROLES.map((role) => {
      const r = settings.roles[role];
      return {
        id: role,
        label: SETTINGS_ROLE_LABEL[role],
        leading: <AgentAvatar role={role === "fixer" ? "implementer" : role} size="sm" />,
        note: project ? (roleChanged(r) ? "changed" : undefined) : r.enabled && !r.enabled.value ? "off" : undefined,
      };
    }),
  };
}

export function deliveryNav(settings: SettingsResponse): SettingsNavItem {
  const n = settings.project ? deliveryChanged(settings.delivery) : 0;
  return { id: "delivery", label: "Delivery", icon: "list-check", note: n ? `${n} changed` : undefined };
}

export function SettingsFrame(props: {
  testId: string;
  scope: { title: ReactNode; subtitle: ReactNode; leading: ReactNode };
  items: SettingsNavItem[];
  page: string;
  onPage: (page: string) => void;
  footer?: ReactNode;
  problem: string | null;
  loading: boolean;
  children: ReactNode;
}) {
  if (props.loading) return <div className="centered">{props.problem ?? <Spinner label="Loading…" />}</div>;
  return (
    <SettingsLayout data-testid={props.testId} scope={props.scope} items={props.items} current={props.page} onSelect={props.onPage} footer={props.footer}>
      {props.children}
    </SettingsLayout>
  );
}
