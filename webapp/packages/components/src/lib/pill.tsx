import styles from './pill.module.css';
import { titleWhenTruncated } from './truncated-title';

export interface PillProps {
  children: string;
  tone?: 'neutral' | 'accent';
}

/** Tiny metadata chip (provider, agent, branch, …). */
export function Pill({ children, tone = 'neutral' }: PillProps) {
  return (
    <span
      className={`${styles.pill} ${styles[tone]}`}
      {...titleWhenTruncated(children)}
    >
      {children}
    </span>
  );
}

export default Pill;
