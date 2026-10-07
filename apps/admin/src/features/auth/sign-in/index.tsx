import { useSearch } from '@tanstack/react-router'
import { AuthLayout } from '../auth-layout'
import { UserAuthForm } from './components/user-auth-form'

export function SignIn() {
  const { redirect } = useSearch({ from: '/(auth)/sign-in' })

  return (
    <AuthLayout>
      <h1 className='mb-8 text-3xl font-bold tracking-tight'>Welcome back</h1>
      <UserAuthForm redirectTo={redirect} />
    </AuthLayout>
  )
}
