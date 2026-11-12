import type { Project } from '@homeshell/model';
import { fireEvent, render, screen, within } from '@testing-library/react';

import { ProjectList } from './project-list';

const projects: Project[] = [
  {
    id: 'p1',
    name: 'alpha',
    path: '/tmp/alpha',
    gitBacked: true,
    state: 'running',
    runtimeState: 'running',
    effectiveState: 'running',
    untrackedWorktrees: [],
    workstreams: [
      {
        id: 'w1',
        name: 'main',
        provider: 'Local System',
        strategyId: 'in-place',
        strategy: 'in place',
        state: 'running',
        runtimeState: 'running',
        effectiveState: 'running',
        sessions: [
          { id: 's1', title: 'work', agent: 'Claude Code', state: 'running' },
        ],
      },
    ],
  },
  {
    id: 'p2',
    name: 'beta',
    path: '/tmp/beta',
    gitBacked: false,
    state: 'idle',
    runtimeState: 'idle',
    effectiveState: 'idle',
    untrackedWorktrees: [],
    workstreams: [],
  },
];

describe('ProjectList', () => {
  it('renders every project with workstream count and git marker', () => {
    render(
      <ProjectList projects={projects} selectedId={null} onSelect={vi.fn()} />,
    );
    const alpha = screen.getByRole('button', { name: /alpha/ });
    expect(within(alpha).getByText('1 workstream · git')).toBeTruthy();
    const beta = screen.getByRole('button', { name: /beta/ });
    expect(within(beta).getByText('0 workstreams')).toBeTruthy();
  });

  it('titles a project name in full on hover while it is cut off', () => {
    render(
      <ProjectList projects={projects} selectedId={null} onSelect={vi.fn()} />,
    );
    const name = screen.getByText('alpha');
    Object.defineProperty(name, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(name);
    expect(name.getAttribute('title')).toBe('alpha');
  });

  it('shows each project effective state as reported by the daemon', () => {
    render(
      <ProjectList projects={projects} selectedId={null} onSelect={vi.fn()} />,
    );
    const alpha = screen.getByRole('button', { name: /alpha/ });
    expect(within(alpha).getByRole('img', { name: 'Running' })).toBeTruthy();
    const beta = screen.getByRole('button', { name: /beta/ });
    expect(within(beta).getByRole('img', { name: 'Idle' })).toBeTruthy();
  });

  it('shows stopped for a project with stopped runtime', () => {
    const stoppedProjects: Project[] = [
      {
        id: 'p3',
        name: 'gamma',
        path: '/tmp/gamma',
        gitBacked: false,
        state: 'done',
        runtimeState: 'stopped',
        effectiveState: 'stopped',
        untrackedWorktrees: [],
        workstreams: [],
      },
    ];
    render(
      <ProjectList
        projects={stoppedProjects}
        selectedId={null}
        onSelect={vi.fn()}
      />,
    );
    const gamma = screen.getByRole('button', { name: /gamma/ });
    expect(within(gamma).getByRole('img', { name: 'Stopped' })).toBeTruthy();
  });

  it('marks the selected project', () => {
    render(
      <ProjectList projects={projects} selectedId="p2" onSelect={vi.fn()} />,
    );
    const beta = screen.getByRole('button', { name: /beta/ });
    expect(beta.getAttribute('aria-current')).toBe('true');
    const alpha = screen.getByRole('button', { name: /alpha/ });
    expect(alpha.getAttribute('aria-current')).toBeNull();
  });

  it('reports selection', () => {
    const onSelect = vi.fn();
    render(
      <ProjectList projects={projects} selectedId="p1" onSelect={onSelect} />,
    );
    fireEvent.click(screen.getByRole('button', { name: /beta/ }));
    expect(onSelect).toHaveBeenCalledWith('p2');
  });

  it('shows an empty hint without projects', () => {
    render(<ProjectList projects={[]} selectedId={null} onSelect={vi.fn()} />);
    expect(screen.getByText('No projects yet.')).toBeTruthy();
  });
});
