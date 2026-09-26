/**
 * What a release build knows about itself. scripts/dist.sh sets both with
 * `bun build --compile --define`; run from source, neither is defined.
 */

declare const DUDE_BUILD_VERSION: string | undefined;
declare const DUDE_BUILD_RELEASE: boolean | undefined;

export const version: string = typeof DUDE_BUILD_VERSION === "string" ? DUDE_BUILD_VERSION : "dev";

/** A compiled release binary, laid out as bin/ beside share/dude/. */
export const isRelease: boolean = typeof DUDE_BUILD_RELEASE === "boolean" && DUDE_BUILD_RELEASE;
