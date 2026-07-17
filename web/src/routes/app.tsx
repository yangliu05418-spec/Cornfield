import {
  createFileRoute,
  Navigate,
  Outlet,
  useLocation,
  useRouter,
} from '@tanstack/react-router'

export const Route = createFileRoute('/app')({ component: AppBoundary })

function AppBoundary() {
  const location = useLocation()
  const router = useRouter()
  if (router.isShell()) return <Outlet />
  if (location.pathname === '/app') return <Navigate to="/app/create" replace />
  return <Outlet />
}
