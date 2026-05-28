import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import LoginForm from './LoginForm';

describe('LoginForm', () => {
  afterEach(() => {
    cleanup();
  });

  it('starts without fake credentials or credential-copy helper text', () => {
    const onLogin = vi.fn<() => Promise<void>>();
    const { container } = render(<LoginForm theme="light" onLogin={onLogin} />);

    const textInput = container.querySelector('input[type="text"]');
    const passwordInput = container.querySelector('input[type="password"]');

    expect(textInput).toBeInstanceOf(HTMLInputElement);
    expect(passwordInput).toBeInstanceOf(HTMLInputElement);
    expect(textInput).toHaveValue('');
    expect(passwordInput).toHaveValue('');
    expect(screen.queryByText(/Demo Credentials Pre-configured/i)).toBeNull();
  });

  it('submits entered credentials through the injected login handler', async () => {
    const user = userEvent.setup();
    const onLogin = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const { container } = render(<LoginForm theme="dark" onLogin={onLogin} />);

    const usernameInput = container.querySelector('input[type="text"]');
    const passwordInput = container.querySelector('input[type="password"]');
    if (!(usernameInput instanceof HTMLInputElement) || !(passwordInput instanceof HTMLInputElement)) {
      throw new Error('Expected username and password inputs to render');
    }

    await user.type(usernameInput, 'operator');
    await user.type(passwordInput, 'secret');
    await user.click(screen.getByRole('button', { name: /authenticate operator session/i }));

    expect(onLogin).toHaveBeenCalledWith('operator', 'secret');
  });

  it('disables submit while login is pending', async () => {
    const user = userEvent.setup();
    let resolveLogin!: () => void;
    const pendingLogin = new Promise<void>((resolve) => {
      resolveLogin = resolve;
    });
    const onLogin = vi.fn<() => Promise<void>>().mockReturnValue(pendingLogin);
    const { container } = render(<LoginForm theme="light" onLogin={onLogin} />);

    const usernameInput = container.querySelector('input[type="text"]');
    const passwordInput = container.querySelector('input[type="password"]');
    if (!(usernameInput instanceof HTMLInputElement) || !(passwordInput instanceof HTMLInputElement)) {
      throw new Error('Expected username and password inputs to render');
    }

    await user.type(usernameInput, 'operator');
    await user.type(passwordInput, 'secret');
    await user.click(screen.getByRole('button', { name: /authenticate operator session/i }));

    expect(screen.getByRole('button', { name: /verifying credentials/i })).toBeDisabled();

    resolveLogin();
    await waitFor(() => expect(screen.getByRole('button', { name: /authenticate operator session/i })).not.toBeDisabled());
  });

  it('shows backend error messages when login fails', async () => {
    const user = userEvent.setup();
    const onLogin = vi.fn<() => Promise<void>>().mockRejectedValue(
      new ApiError(401, 'UNAUTHORIZED', 'admin login required', null),
    );
    const { container } = render(<LoginForm theme="light" onLogin={onLogin} />);

    const usernameInput = container.querySelector('input[type="text"]');
    const passwordInput = container.querySelector('input[type="password"]');
    if (!(usernameInput instanceof HTMLInputElement) || !(passwordInput instanceof HTMLInputElement)) {
      throw new Error('Expected username and password inputs to render');
    }

    await user.type(usernameInput, 'operator');
    await user.type(passwordInput, 'wrong');
    await user.click(screen.getByRole('button', { name: /authenticate operator session/i }));

    expect(await screen.findByText('admin login required')).toBeVisible();
  });
});
