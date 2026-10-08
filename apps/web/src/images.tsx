/**
 * Picking an image wherever one is asked for — a project's runtime and
 * previews, an agent role's — over the organisation's library, and what a
 * field shows for an image typed by hand before the library existed: read
 * only, with a way to clear it. A new image is never typed: it is picked.
 */

import { useCallback, useEffect, useState } from "react";
import { ImagePicker, type ImageChoiceView } from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";
import type { ImageChoice } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";

export interface ImageChoices {
  images: ImageChoice[] | null;
  defaultImageId: string | null;
  reload: () => void;
}

/** The organisation's images, once for a screen; none when they fail to load. */
export function useImageChoices(client: ApiClient): ImageChoices {
  const [state, setState] = useState<{ images: ImageChoice[] | null; defaultImageId: string | null }>({ images: null, defaultImageId: null });
  const reload = useCallback(() => {
    client.imageChoices().then(
      (r) => setState({ images: r.images, defaultImageId: r.defaultImageId }),
      () => setState({ images: [], defaultImageId: null }),
    );
  }, [client]);
  useEffect(reload, [reload]);
  return { ...state, reload };
}

/** An image as the picker lists it. */
export const choiceView = (i: ImageChoice): ImageChoiceView => ({
  id: i.id,
  name: i.name,
  description: i.description,
  version: i.version,
  isDefault: i.isDefault,
  archived: i.archived,
  status: i.status,
  canRunContainers: i.canRunContainers,
});

/** Whether the library image `id` can run containers now (its published version). */
export function canRunContainers(images: readonly ImageChoice[] | null, id: string | null): boolean {
  return Boolean(id && images?.find((x) => x.id === id)?.canRunContainers);
}

/** An image's name and version now, for words: "acme-base v7". */
export function imageWords(images: readonly ImageChoice[] | null, id: string | null): string | null {
  const i = id ? images?.find((x) => x.id === id) : undefined;
  return i ? `${i.name}${i.version ? ` v${i.version}` : ""}` : null;
}

export interface ImageFieldProps {
  images: readonly ImageChoice[] | null;
  value: string | null;
  onChange: (id: string | null) => void;
  label: string;
  /** An image typed before the library, still in use when nothing is picked. */
  legacy?: string | null | undefined;
  onClearLegacy?: (() => void) | undefined;
  /**
   * A library image that wins over the typed one even with nothing picked
   * here (the organisation's default base): every library image comes
   * before any typed one.
   */
  shadowedBy?: string | null | undefined;
  /** What picking none means here. */
  noneLabel?: string | undefined;
  allowNone?: string | undefined;
  orgName: string;
  disabled?: boolean | undefined;
  onManage?: (() => void) | undefined;
  onCreateFrom?: ((text: string) => void) | undefined;
  testId?: string | undefined;
  /**
   * Who starts containers in a picked image that can run them, for the
   * line under the picker: "this project’s agents", "testers". None: no line.
   */
  containersFor?: string | undefined;
}

/** The picker, with a typed image from before the library said and clearable under it. */
export function ImageField({ images, value, onChange, label, legacy, onClearLegacy, shadowedBy, noneLabel, allowNone, orgName, disabled, onManage, onCreateFrom, testId, containersFor }: ImageFieldProps) {
  const used = value === null && !shadowedBy;
  const views = (images ?? []).map(choiceView);
  return (
    <div className="imageField">
      <ImagePicker
        images={views}
        value={value}
        onChange={onChange}
        label={label}
        heading={`${orgName}’s images`}
        noneLabel={used && legacy ? undefined : noneLabel}
        allowNone={allowNone}
        disabled={disabled || images === null}
        manage={onManage ? { label: "Manage images", onClick: onManage } : undefined}
        onCreateFrom={onCreateFrom}
        data-testid={testId}
      />
      {containersFor && canRunContainers(images, value) ? (
        <p className="imageNested" data-testid={testId ? `${testId}-containers` : undefined}>
          <Icon name="cube" size={14} />
          This image can run containers: {containersFor} can start containers.
        </p>
      ) : null}
      {legacy ? (
        <p className="imageLegacy" data-testid={testId ? `${testId}-legacy` : undefined}>
          {used ? "Uses" : "Not used:"} <code>{legacy}</code> (typed by hand).{" "}
          {used ? "It can’t run containers. Pick an image to replace it." : value ? "The picked image wins over it." : `${shadowedBy} wins over it.`}
          {onClearLegacy && !disabled ? (
            <Button size="sm" variant="quiet" onClick={onClearLegacy} data-testid={testId ? `${testId}-clear-legacy` : undefined}>
              Clear it
            </Button>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}
