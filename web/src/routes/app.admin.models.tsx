import { createFileRoute, redirect } from '@tanstack/react-router'

export const Route = createFileRoute('/app/admin/models')({
  beforeLoad: () => {
    throw redirect({ to: '/app/admin/providers', replace: true })
  },
})
