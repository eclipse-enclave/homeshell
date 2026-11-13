const withoutTrailingSlash = (path: string) =>
  path.length > 1 && path.endsWith('/') ? path.slice(0, -1) : path;

/** `../name` for a sibling of the project folder, else the absolute path. */
export function displayPath(dir: string, projectPath: string): string {
  const folder = withoutTrailingSlash(dir);
  const project = withoutTrailingSlash(projectPath);
  const parent = project.slice(0, project.lastIndexOf('/'));
  const name = folder.slice(parent.length + 1);
  if (
    parent &&
    folder.startsWith(`${parent}/`) &&
    name &&
    !name.includes('/')
  ) {
    return `../${name}`;
  }
  return folder;
}
