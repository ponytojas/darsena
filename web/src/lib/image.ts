/** Splits a saved reference like `docker.io/library/nginx:1.27@sha256:…`. */
function split(reference: string) {
  const [nameTag, digest = ""] = reference.split("@");
  const slash = nameTag.lastIndexOf("/");
  const colon = nameTag.lastIndexOf(":");
  const hasTag = colon > slash;
  const name = (hasTag ? nameTag.slice(0, colon) : nameTag).replace(
    /^docker\.io\/(library\/)?/,
    "",
  );
  return { name, tag: hasTag ? nameTag.slice(colon + 1) : "", digest };
}

/** How an image reads to a person: `nginx:1.27` as written, or `nginx` without a tag. */
export function imageLabel(reference: string) {
  const { name, tag } = split(reference);
  return tag ? `${name}:${tag}` : name;
}

/** Compact label for lists: without a tag, a short digest keeps different pins distinguishable. */
export function imageSummary(reference: string) {
  return split(reference).tag ? imageLabel(reference) : `${imageLabel(reference)} · ${shortDigest(reference)}`;
}

/** The pinned digest, shortened for secondary display. */
export function shortDigest(reference: string) {
  const digest = split(reference).digest;
  return digest ? `sha256:${digest.replace(/^sha256:/, "").slice(0, 12)}` : "";
}

/**
 * Starting value of an image field for a saved definition: the tag if there
 * is one (editing it re-pins to that tag's current digest), else the pinned
 * reference, so leaving it untouched never changes what runs.
 */
export function imageInput(reference: string) {
  const { name, tag, digest } = split(reference);
  return tag ? `${name}:${tag}` : digest ? `${name}@${digest}` : name;
}
