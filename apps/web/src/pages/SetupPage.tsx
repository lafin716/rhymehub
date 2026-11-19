import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { errorMessage } from '../api/client';
import { AuthCard } from '../components/AuthCard';
import { Button, ErrorMessage, Field, Input } from '../components/ui';
import { useAuth } from '../state/AuthContext';

const MIN_PASSWORD_LENGTH = 8;

export function SetupPage() {
  const { status, setup } = useAuth();
  const navigate = useNavigate();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (status === 'authenticated') return <Navigate to="/" replace />;
  if (status === 'anonymous') return <Navigate to="/login" replace />;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password.length < MIN_PASSWORD_LENGTH) {
      setError(`Use at least ${MIN_PASSWORD_LENGTH} characters for the password.`);
      return;
    }
    if (password !== confirm) {
      setError('The passwords do not match.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await setup(username.trim(), password);
      navigate('/', { replace: true });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard title="Create the admin account" subtitle="First run: this account manages rhymehub.">
      <form className="flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
        <Field id="setup-username" label="Username">
          <Input id="setup-username" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} />
        </Field>
        <Field id="setup-password" label="Password" hint={`At least ${MIN_PASSWORD_LENGTH} characters.`}>
          <Input
            id="setup-password"
            type="password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field id="setup-confirm" label="Confirm password">
          <Input
            id="setup-confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </Field>
        <ErrorMessage error={error} />
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? 'Creating…' : 'Create account'}
        </Button>
      </form>
    </AuthCard>
  );
}
