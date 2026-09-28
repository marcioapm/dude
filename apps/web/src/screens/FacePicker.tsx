/**
 * Choosing a face: a person's photo or a project's image. The picked file
 * is cropped to its centre and resized here, in the browser, to a small
 * square JPEG, and uploaded as that image; the backend keeps it in object
 * storage and serves it back under a token.
 */

import { useRef, type ReactNode } from "react";
import { Button } from "@dude/design-system/primitives";

/** The side, in pixels: twice the largest face drawn, for sharp screens. */
const FACE_PX = 160;

/** A picked image as a small square JPEG, cropped to its centre. */
export async function squareImage(file: File, px = FACE_PX): Promise<Blob> {
  const bitmap = await createImageBitmap(file);
  const side = Math.min(bitmap.width, bitmap.height);
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = px;
  canvas.getContext("2d")!.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, px, px);
  bitmap.close();
  return new Promise((resolve, reject) =>
    canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("could not read that image"))), "image/jpeg", 0.85));
}

/**
 * The face, an Upload button and, when there is an image, a way back to
 * initials. `onPick` gets the resized image to save, as a promise so a
 * file that is no image fails where the save's problem shows; `onRemove`
 * clears it.
 */
export function FacePicker({ face, hasImage, busy, disabled, onPick, onRemove, testId }: {
  face: ReactNode;
  hasImage: boolean;
  busy: boolean;
  disabled?: boolean;
  onPick: (image: Promise<Blob>) => void;
  onRemove: () => void;
  testId: string;
}) {
  const file = useRef<HTMLInputElement>(null);
  return (
    <div className="profilePhoto">
      {face}
      {disabled ? null : (
        <>
          <Button variant="secondary" disabled={busy} onClick={() => file.current?.click()} data-testid={`${testId}-upload`}>
            Upload…
          </Button>
          {hasImage ? (
            <Button variant="quiet" disabled={busy} onClick={onRemove} data-testid={`${testId}-remove`}>
              Use initials
            </Button>
          ) : null}
          <input ref={file} type="file" accept="image/png,image/jpeg,image/webp,image/gif" hidden data-testid={`${testId}-file`}
            onChange={(e) => {
              const picked = e.target.files?.[0];
              e.target.value = "";
              if (picked) onPick(squareImage(picked));
            }} />
        </>
      )}
    </div>
  );
}
