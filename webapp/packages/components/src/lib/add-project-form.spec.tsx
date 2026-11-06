import { fireEvent, render, screen, waitFor } from '@testing-library/react';

import { AddProjectForm } from './add-project-form';
import type { FormHandle } from './create-form';
import { pickFolderNatively } from './native-folder-picker';

describe('AddProjectForm', () => {
    it('renders path and name inputs', () => {
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} />);
        expect(screen.getByPlaceholderText('/path/to/project')).toBeTruthy();
        expect(
            screen.getByPlaceholderText('defaults to directory name')
        ).toBeTruthy();
    });

    it('gives its buttons the letters Alt presses them by', () => {
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} />);
        const letter = (name: string) =>
            screen.getByRole('button', { name }).getAttribute('data-mnemonic');
        expect([letter('Browse'), letter('Cancel'), letter('Add')]).toEqual(['b', 'c', 'a']);
    });

    it('submits path and name', async () => {
        const onSubmit = vi.fn().mockResolvedValue(null);
        render(<AddProjectForm onSubmit={onSubmit} onCancel={vi.fn()} />);

        fireEvent.change(screen.getByPlaceholderText('/path/to/project'), {
            target: { value: '/tmp/my-project' },
        });
        fireEvent.change(
            screen.getByPlaceholderText('defaults to directory name'),
            { target: { value: 'My Project' } }
        );
        fireEvent.click(screen.getByRole('button', { name: 'Add' }));

        await waitFor(() => {
            expect(onSubmit).toHaveBeenCalledWith({
                path: '/tmp/my-project',
                name: 'My Project',
            });
        });
    });

    it('shows a client-side error for an empty path', async () => {
        const onSubmit = vi.fn().mockResolvedValue(null);
        render(<AddProjectForm onSubmit={onSubmit} onCancel={vi.fn()} />);

        fireEvent.click(screen.getByRole('button', { name: 'Add' }));
        const alert = await screen.findByRole('alert');
        expect(alert.textContent).toBe('Path is required.');
        expect(onSubmit).not.toHaveBeenCalled();
    });

    it('shows a server-side error returned by onSubmit', async () => {
        const onSubmit = vi.fn().mockResolvedValue('path does not exist');
        render(<AddProjectForm onSubmit={onSubmit} onCancel={vi.fn()} />);

        fireEvent.change(screen.getByPlaceholderText('/path/to/project'), {
            target: { value: '/bad/path' },
        });
        fireEvent.click(screen.getByRole('button', { name: 'Add' }));

        const alert = await screen.findByRole('alert');
        expect(alert.textContent).toBe('path does not exist');
    });

    // The error was about the path that was sent, not the one being typed.
    it.each([
        ['path', '/path/to/project'],
        ['name', 'defaults to directory name'],
    ])('drops the error once the %s is edited', async (_field, placeholder) => {
        const onSubmit = vi.fn().mockResolvedValue('path does not exist');
        render(<AddProjectForm onSubmit={onSubmit} onCancel={vi.fn()} />);
        fireEvent.change(screen.getByPlaceholderText('/path/to/project'), {
            target: { value: 'relative' },
        });
        fireEvent.click(screen.getByRole('button', { name: 'Add' }));
        await screen.findByRole('alert');

        fireEvent.change(screen.getByPlaceholderText(placeholder), {
            target: { value: '/abs/path' },
        });
        expect(screen.queryByRole('alert')).toBeNull();
    });

    it('calls onCancel when Cancel is clicked', () => {
        const onCancel = vi.fn();
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={onCancel} />);
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
        expect(onCancel).toHaveBeenCalled();
    });

    // Offline: the form exists to send a path somewhere, so while the daemon is
    // gone the fields and submit go inert — but Cancel stays live, since backing
    // out needs nothing, and what is typed is kept rather than cleared.
    describe('when the daemon is unreachable', () => {
        const REASON = 'The daemon is offline';

        it('makes the fields and submit inert, with the reason on them', () => {
            render(
                <AddProjectForm
                    onSubmit={vi.fn()}
                    onCancel={vi.fn()}
                    disabledReason={REASON}
                />,
            );
            const path = screen.getByPlaceholderText(
                '/path/to/project',
            ) as HTMLInputElement;
            expect(path.readOnly).toBe(true);
            expect(path.title).toBe(REASON);
            const add = screen.getByRole('button', { name: 'Add' });
            expect(add.getAttribute('aria-disabled')).toBe('true');
            expect(add.title).toBe(REASON);
        });

        it('leaves Cancel usable', () => {
            const onCancel = vi.fn();
            render(
                <AddProjectForm
                    onSubmit={vi.fn()}
                    onCancel={onCancel}
                    disabledReason={REASON}
                />,
            );
            fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
            expect(onCancel).toHaveBeenCalled();
        });

        it('keeps what was already typed', () => {
            const { rerender } = render(
                <AddProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} />,
            );
            fireEvent.change(screen.getByPlaceholderText('/path/to/project'), {
                target: { value: '/typed/before/the/drop' },
            });
            rerender(
                <AddProjectForm
                    onSubmit={vi.fn()}
                    onCancel={vi.fn()}
                    disabledReason={REASON}
                />,
            );
            const path = screen.getByPlaceholderText(
                '/path/to/project',
            ) as HTMLInputElement;
            expect(path.readOnly).toBe(true);
            expect(path.value).toBe('/typed/before/the/drop');
            expect(document.activeElement).toBe(path);
        });
    });
});

// Browse asks the host desktop for its own folder chooser and only falls back
// to the in-app picker when there is not one. The in-app picker is identified
// by its dialog, which the native path must never show.
describe('AddProjectForm Browse', () => {
    const pathBox = () => screen.getByPlaceholderText('/path/to/project') as HTMLInputElement;

    it('fills the path from the native chooser without showing the in-app picker', async () => {
        const pickNatively = vi
            .fn()
            .mockResolvedValue({ outcome: 'selected', path: '/home/user/code' });
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={pickNatively}
            />
        );

        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));

        await waitFor(() => expect(pathBox().value).toBe('/home/user/code'));
        expect(screen.queryByRole('dialog', { name: 'Select folder' })).toBeNull();
    });

    // What is already typed is where the dialog should open.
    it('opens the chooser at the path already typed', async () => {
        const pickNatively = vi.fn().mockResolvedValue({ outcome: 'cancelled' });
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={pickNatively}
            />
        );

        fireEvent.change(pathBox(), { target: { value: '  /home/user/code  ' } });
        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));

        await waitFor(() =>
            expect(pickNatively).toHaveBeenCalledWith(
                '/home/user/code',
                expect.any(AbortSignal)
            )
        );
    });

    // Dismissing the desktop dialog means "not now", so popping the in-app
    // picker instead would read as the app ignoring the dismissal.
    it('does nothing when the user dismisses the native chooser', async () => {
        const pickNatively = vi.fn().mockResolvedValue({ outcome: 'cancelled' });
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={pickNatively}
            />
        );

        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));

        await waitFor(() => expect(pickNatively).toHaveBeenCalled());
        expect(pathBox().value).toBe('');
        expect(screen.queryByRole('dialog', { name: 'Select folder' })).toBeNull();
    });

    // A host with no dialog tool, or a daemon reached over the network: the
    // in-app picker still works, so browsing must not become impossible.
    it('falls back to the in-app picker when there is no native chooser', async () => {
        const pickNatively = vi.fn().mockResolvedValue({ outcome: 'unavailable' });
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={pickNatively}
            />
        );

        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));

        expect(
            await screen.findByRole('dialog', { name: 'Select folder' })
        ).toBeTruthy();
    });

    it('opens the in-app picker on the typed path', async () => {
        const fetchMock = vi.fn(async () => ({
            ok: true,
            json: async () => ({ path: '/srv/typed', parent: '/srv', entries: [] }),
        }));
        vi.stubGlobal('fetch', fetchMock);
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={vi.fn().mockResolvedValue({ outcome: 'unavailable' })}
            />
        );
        fireEvent.change(screen.getByPlaceholderText('/path/to/project'), {
            target: { value: ' /srv/typed ' },
        });
        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));

        expect(await screen.findByTestId('current-path')).toBeTruthy();
        expect(fetchMock).toHaveBeenCalledWith('/api/browse?path=%2Fsrv%2Ftyped');
        vi.unstubAllGlobals();
    });

    it('names the path box Path, apart from its Browse button', () => {
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} />);
        expect(
            screen.getByRole('textbox', { name: 'Path' }).getAttribute('placeholder')
        ).toBe('/path/to/project');
    });

    // The native dialog is a separate window, so the button is the only thing
    // that can show one is already open — and stop a second one being opened.
    it('marks Browse busy while the dialog is up', async () => {
        let answer: (result: { outcome: string }) => void = () => undefined;
        const pickNatively = vi.fn(
            () => new Promise((resolve) => (answer = resolve))
        );
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={pickNatively as never}
            />
        );

        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));

        const busy = await screen.findByRole('button', { name: 'Browsing…' });
        expect(busy.getAttribute('aria-disabled')).toBe('true');
        fireEvent.click(busy);
        expect(pickNatively).toHaveBeenCalledTimes(1);

        answer({ outcome: 'cancelled' });
        await waitFor(() =>
            expect(screen.getByRole('button', { name: 'Browse' })).toBeTruthy()
        );
    });
});

describe('AddProjectForm when the daemon is unreachable', () => {
    const REASON = 'The daemon is offline';

    it('makes Add and Browse inert, since both need the daemon', () => {
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                disabledReason={REASON}
            />
        );
        for (const name of ['Add', 'Browse']) {
            const button = screen.getByRole('button', { name });
            expect(button.getAttribute('aria-disabled')).toBe('true');
            expect(button.getAttribute('title')).toBe(REASON);
        }
    });

    // The fields exist to be submitted, so they go with the button.
    it('makes the text fields inert too', () => {
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                disabledReason={REASON}
            />
        );
        for (const placeholder of [
            '/path/to/project',
            'defaults to directory name',
        ]) {
            const field = screen.getByPlaceholderText(
                placeholder
            ) as HTMLInputElement;
            expect(field.readOnly).toBe(true);
            expect(field.getAttribute('aria-disabled')).toBe('true');
            expect(field.getAttribute('title')).toBe(REASON);
        }
    });

    // Backing out needs nothing from the daemon.
    it('leaves Cancel alone', () => {
        const onCancel = vi.fn();
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={onCancel}
                disabledReason={REASON}
            />
        );
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
        expect(onCancel).toHaveBeenCalled();
    });

    // The dialog is a window on the host's screen and the daemon holds the
    // request open for as long as it is up, so a form that goes away without
    // aborting leaves it there with nobody waiting on it. The daemon runs the
    // helper on the request's context, so dropping the request closes it.
    it('aborts an open dialog when the form goes away', async () => {
        let seen: AbortSignal | undefined;
        const pickNatively = vi.fn(
            (_path?: string, signal?: AbortSignal) =>
                new Promise<never>(() => {
                    seen = signal;
                })
        );
        const { unmount } = render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={pickNatively as never}
            />
        );

        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));
        await waitFor(() => expect(seen).toBeDefined());
        expect(seen?.aborted).toBe(false);

        unmount();
        expect(seen?.aborted).toBe(true);
    });

    // The picker passes the signal through to the request, which is what makes
    // the abort above reach the daemon at all.
    it('hands the signal to the request', async () => {
        let init: RequestInit | undefined;
        const fetchMock = vi.fn((_url: string, options?: RequestInit) => {
            init = options;
            return new Promise<Response>(() => undefined);
        });
        vi.stubGlobal('fetch', fetchMock);
        const controller = new AbortController();
        void pickFolderNatively('/start', controller.signal);

        await waitFor(() => expect(fetchMock).toHaveBeenCalled());
        expect(init?.signal).toBe(controller.signal);
        vi.unstubAllGlobals();
    });
});

describe('AddProjectForm by keyboard', () => {
    const pathBox = () =>
        screen.getByPlaceholderText('/path/to/project') as HTMLInputElement;

    it('opens on the path', () => {
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} />);
        expect(document.activeElement).toBe(pathBox());
    });

    it('focuses the path again through its ref', () => {
        const ref = { current: null as FormHandle | null };
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={vi.fn()} ref={ref} />);
        screen.getByPlaceholderText('defaults to directory name').focus();
        ref.current?.focus();
        expect(document.activeElement).toBe(pathBox());
    });

    it('cancels on Escape', () => {
        const onCancel = vi.fn();
        render(<AddProjectForm onSubmit={vi.fn()} onCancel={onCancel} />);
        fireEvent.keyDown(pathBox(), { key: 'Escape' });
        expect(onCancel).toHaveBeenCalled();
    });

    it('closes only the folder picker on Escape inside it', async () => {
        const onCancel = vi.fn();
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={onCancel}
                pickNatively={vi.fn().mockResolvedValue({ outcome: 'unavailable' })}
            />
        );
        fireEvent.click(screen.getByRole('button', { name: 'Browse' }));
        const picker = await screen.findByRole('dialog', { name: 'Select folder' });

        fireEvent.keyDown(picker, { key: 'Escape' });
        expect(screen.queryByRole('dialog')).toBeNull();
        expect(onCancel).not.toHaveBeenCalled();
    });

    it('focuses the path the native chooser fills in', async () => {
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={vi
                    .fn()
                    .mockResolvedValue({ outcome: 'selected', path: '/home/user/code' })}
            />
        );
        const browse = screen.getByRole('button', { name: 'Browse' });
        browse.focus();
        fireEvent.click(browse);

        await waitFor(() => expect(pathBox().value).toBe('/home/user/code'));
        expect(document.activeElement).toBe(pathBox());
    });

    it('keeps Browse focused while the native chooser is up', async () => {
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={vi.fn(() => new Promise<never>(() => undefined))}
            />
        );
        const browse = screen.getByRole('button', { name: 'Browse' });
        browse.focus();
        fireEvent.click(browse);

        const busy = await screen.findByRole('button', { name: 'Browsing…' });
        expect(document.activeElement).toBe(busy);
    });

    it('gives focus back to Browse when the folder picker closes', async () => {
        render(
            <AddProjectForm
                onSubmit={vi.fn()}
                onCancel={vi.fn()}
                pickNatively={vi.fn().mockResolvedValue({ outcome: 'unavailable' })}
            />
        );
        const browse = screen.getByRole('button', { name: 'Browse' });
        browse.focus();
        fireEvent.click(browse);
        const picker = await screen.findByRole('dialog', { name: 'Select folder' });

        fireEvent.keyDown(picker, { key: 'Escape' });
        expect(document.activeElement).toBe(browse);
    });

    it('keeps Add focused while the request is out', () => {
        render(
            <AddProjectForm
                onSubmit={vi.fn(() => new Promise<null>(() => undefined))}
                onCancel={vi.fn()}
            />
        );
        fireEvent.change(pathBox(), { target: { value: '/tmp/p' } });
        const add = screen.getByRole('button', { name: 'Add' });
        add.focus();
        fireEvent.click(add);
        expect(add.getAttribute('aria-disabled')).toBe('true');
        expect(pathBox().readOnly).toBe(true);
        expect(document.activeElement).toBe(add);
    });
});
