import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, UserPlus } from 'lucide-react'
import { toast } from 'sonner'
import { roleLabels, useAuthStore, type StaffRole } from '@/stores/auth-store'
import { api } from '@/lib/api'
import { dateTime } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { ListRows } from '@/components/list-state'
import { Page } from '@/components/page'

type StaffMember = {
  userId: string
  username: string | null
  displayName: string
  konamiId: string | null
  role: StaffRole
  grantedAt: string
  grantedBy: string | null
}

type StaffList = { data: StaffMember[]; roles: StaffRole[] }

const roles: StaffRole[] = ['support', 'reviewer', 'operator', 'admin']

export function StaffPage() {
  const queryClient = useQueryClient()
  const me = useAuthStore((state) => state.staff)
  const [granting, setGranting] = useState<{
    konamiId: string
    role: StaffRole
  } | null>(null)
  const [revoking, setRevoking] = useState<StaffMember | null>(null)
  const { data, isLoading, error } = useQuery({
    queryKey: ['staff'],
    queryFn: () => api<StaffList>('/v1/admin/staff'),
  })

  const grant = useMutation({
    mutationFn: (input: { konamiId: string; role: StaffRole }) =>
      api<StaffList>('/v1/admin/staff', { method: 'POST', body: input }),
    onSuccess: (list) => {
      queryClient.setQueryData(['staff'], list)
      setGranting(null)
      toast.success('Role saved.')
    },
  })
  const revoke = useMutation({
    mutationFn: (userId: string) =>
      api(`/v1/admin/staff/${userId}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['staff'] })
      setRevoking(null)
      toast.success('Staff access removed.')
    },
  })

  return (
    <Page
      title='Staff & roles'
      permission='staff.manage'
      actions={
        <Button onClick={() => setGranting({ konamiId: '', role: 'reviewer' })}>
          <UserPlus /> Add staff
        </Button>
      }
    >
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Konami ID</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Granted</TableHead>
              <TableHead className='text-end'>Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={isLoading}
              error={error}
              empty='No staff yet.'
              columns={5}
              count={data?.data.length ?? 0}
            />
            {data?.data.map((member) => {
              const self = member.userId === me?.userId
              return (
                <TableRow key={member.userId}>
                  <TableCell>
                    <div className='font-medium'>{member.displayName}</div>
                    {member.username && (
                      <div className='text-xs text-muted-foreground'>
                        @{member.username}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className='font-mono text-xs'>
                    {member.konamiId ?? '—'}
                  </TableCell>
                  <TableCell>
                    <Select
                      value={member.role}
                      disabled={self || grant.isPending || !member.konamiId}
                      onValueChange={(role) =>
                        grant.mutate({
                          konamiId: member.konamiId!,
                          role: role as StaffRole,
                        })
                      }
                    >
                      <SelectTrigger className='w-36'>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {roles.map((role) => (
                          <SelectItem key={role} value={role}>
                            {roleLabels[role]}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </TableCell>
                  <TableCell className='text-xs text-muted-foreground'>
                    {dateTime(member.grantedAt)}
                    {member.grantedBy && <div>by @{member.grantedBy}</div>}
                  </TableCell>
                  <TableCell className='text-end'>
                    {self ? (
                      <span className='text-xs text-muted-foreground'>You</span>
                    ) : (
                      <Button
                        variant='ghost'
                        size='sm'
                        className='text-destructive'
                        onClick={() => setRevoking(member)}
                      >
                        Remove
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>

      <Dialog
        open={!!granting}
        onOpenChange={(open) => !open && setGranting(null)}
      >
        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>Add staff</DialogTitle>
          </DialogHeader>
          {granting && (
            <form
              id='grant-staff'
              className='grid gap-4'
              onSubmit={(event) => {
                event.preventDefault()
                grant.mutate(granting)
              }}
            >
              <div className='grid gap-2'>
                <Label htmlFor='konami-id'>Konami ID</Label>
                <Input
                  id='konami-id'
                  placeholder='ABCD-1234-EFGH'
                  value={granting.konamiId}
                  onChange={(event) =>
                    setGranting({ ...granting, konamiId: event.target.value })
                  }
                  required
                />
              </div>
              <div className='grid gap-2'>
                <Label>Role</Label>
                <Select
                  value={granting.role}
                  onValueChange={(role) =>
                    setGranting({ ...granting, role: role as StaffRole })
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {roles.map((role) => (
                      <SelectItem key={role} value={role}>
                        {roleLabels[role]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </form>
          )}
          <DialogFooter>
            <Button type='submit' form='grant-staff' disabled={grant.isPending}>
              {grant.isPending && <Loader2 className='animate-spin' />}
              Grant role
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(open) => !open && setRevoking(null)}
        title='Remove staff access'
        desc={`${revoking?.displayName ?? ''} will lose dashboard access immediately. Their player account is not affected.`}
        confirmText='Remove'
        destructive
        isLoading={revoke.isPending}
        handleConfirm={() => revoking && revoke.mutate(revoking.userId)}
      />
    </Page>
  )
}
