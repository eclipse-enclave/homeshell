import type { Project } from '@homeshell/model';
import { StatusDot } from './status-dot';
import styles from './project-list.module.css';
import { titleWhenTruncated } from './truncated-title';

export interface ProjectListProps {
  projects: readonly Project[];
  selectedId: string | null;
  onSelect: (projectId: string) => void;
}

/**
 * The selectable project list for the sidebar. Each entry shows the
 * project's aggregate state as reported by the daemon.
 */
export function ProjectList({ projects, selectedId, onSelect }: ProjectListProps) {
  if (projects.length === 0) {
    return <p className={styles.empty}>No projects yet.</p>;
  }
  return (
    <ul className={styles.list}>
      {projects.map((project) => {
        const selected = project.id === selectedId;
        const count = project.workstreams.length;
        return (
          <li key={project.id}>
            <button
              type="button"
              className={selected ? `${styles.item} ${styles.selected}` : styles.item}
              aria-current={selected ? 'true' : undefined}
              onClick={() => onSelect(project.id)}
            >
              <StatusDot state={project.effectiveState} />
              <span className={styles.itemBody}>
                <span
                  className={styles.name}
                  {...titleWhenTruncated(project.name)}
                >
                  {project.name}
                </span>
                <span className={styles.meta}>
                  {count === 1 ? '1 workstream' : `${count} workstreams`}
                  {project.gitBacked ? ' · git' : ''}
                </span>
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

export default ProjectList;
