/**
 * What a release build knows about itself. scripts/dist.sh sets it with
 * `bun build --compile --define`; run from source, it is not defined.
 */

declare const DUDE_BUILD_VERSION: string | undefined;

export const version: string = typeof DUDE_BUILD_VERSION === "string" ? DUDE_BUILD_VERSION : "dev";
