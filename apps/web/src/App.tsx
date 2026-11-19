import { Navigate, Route, Routes } from 'react-router';
import { AuthGate } from './components/AuthGate';
import { AuthProvider } from './state/AuthContext';
import { AgentsPage } from './pages/AgentsPage';
import { DashboardPage } from './pages/DashboardPage';
import { InboxPage } from './pages/InboxPage';
import { LoginPage } from './pages/LoginPage';
import { NotificationsPage } from './pages/NotificationsPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
import { ProjectsPage } from './pages/ProjectsPage';
import { RegisterProjectPage } from './pages/RegisterProjectPage';
import { ReleasesPage } from './pages/ReleasesPage';
import { SettingsPage } from './pages/SettingsPage';
import { SetupPage } from './pages/SetupPage';
import { TaskPage } from './pages/TaskPage';

export default function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/setup" element={<SetupPage />} />
        <Route path="/login" element={<LoginPage />} />
        <Route element={<AuthGate />}>
          <Route index element={<DashboardPage />} />
          <Route path="projects" element={<ProjectsPage />} />
          <Route path="projects/new" element={<RegisterProjectPage />} />
          <Route path="projects/:id" element={<ProjectDetailPage />} />
          <Route path="tasks/:id" element={<TaskPage />} />
          <Route path="inbox" element={<InboxPage />} />
          <Route path="agents" element={<AgentsPage />} />
          <Route path="releases" element={<ReleasesPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="notifications" element={<NotificationsPage />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </AuthProvider>
  );
}
