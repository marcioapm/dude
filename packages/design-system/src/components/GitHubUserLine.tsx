import type { HTMLAttributes, ReactNode } from "react";
import { EntityLine } from "./EntityLine.tsx";
import { PersonAvatar, type PersonAvatarSize } from "./PersonAvatar.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import styles from "./GitHubUserLine.module.css";

/** Someone on GitHub, or a team ("org/slug"), as GitHub names them. */
export interface GitHubUser {
  readonly login: string;
  readonly name?: string | undefined;
  readonly avatarUrl?: string | undefined;
  readonly team?: boolean | undefined;
}

export interface GitHubUserLineProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly user: GitHubUser;
  /** After the login, muted: why GitHub suggests them, a team's size. */
  readonly detail?: ReactNode;
  /** Pushed to the end: a badge. */
  readonly trailing?: ReactNode;
  /** The face; 24 in a list, 20 in a fact's line. */
  readonly size?: PersonAvatarSize | undefined;
  /** One line, the login after the name (a list of picks); two by default. */
  readonly inline?: boolean | undefined;
}

/** A GitHub account's face: a person's circle, or a team's rounded square. */
export function GitHubFace({ user, size = 24 }: { readonly user: GitHubUser; readonly size?: PersonAvatarSize | undefined }) {
  const name = user.name || user.login;
  return user.team ? (
    <ProjectAvatar project={{ id: user.login, name, imageUrl: user.avatarUrl ?? null }} size={size} />
  ) : (
    <PersonAvatar person={{ id: user.login, name, photoUrl: user.avatarUrl ?? null }} size={size} ring={false} title={user.login} />
  );
}

/**
 * Someone on GitHub in a row: their face, their name in strong ink, and
 * under it their login (mono, it is what GitHub knows them by) and why
 * they are offered. A team is the same, its face a rounded square. With
 * no name, the login is the name.
 */
export function GitHubUserLine({ user, detail, trailing, size = 24, inline, ...rest }: GitHubUserLineProps) {
  const login = <span className={styles["login"]}>{user.login}</span>;
  const line = { size: "sm" as const, lead: <GitHubFace user={user} size={size} />, trailing, ...rest };
  if (!user.name || user.name === user.login) return <EntityLine {...line} name={login} detail={inline ? undefined : detail} />;
  if (inline) return <EntityLine {...line} name={<>{user.name}<span className={styles["inlineLogin"]}> {login}</span></>} />;
  return <EntityLine {...line} name={user.name} detail={<>{login}{detail ? <> · {detail}</> : null}</>} />;
}
