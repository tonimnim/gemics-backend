import {
  BadgeCheck,
  Banknote,
  Gavel,
  LayoutDashboard,
  ReceiptText,
  ShieldAlert,
  Trophy,
  UserCog,
} from 'lucide-react'
import { usePermissions, type Permission } from '@/stores/auth-store'
import { useOverview } from '@/features/overview/api'
import { type NavGroup, type NavLink } from '../types'

type GatedLink = NavLink & {
  permission: Permission
  count?: (queues: QueueCounts) => number
}

type QueueCounts = {
  resultReviews: number
  accountVerifications: number
  paymentReviews: number
  refunds: number
}

/** The whole dashboard. Each page appears only for staff who hold its permission. */
const navigation: { title: string; items: GatedLink[] }[] = [
  {
    title: 'General',
    items: [
      {
        title: 'Overview',
        url: '/',
        icon: LayoutDashboard,
        permission: 'overview.view',
      },
      {
        title: 'Competitions',
        url: '/competitions',
        icon: Trophy,
        permission: 'competition.manage',
      },
    ],
  },
  {
    title: 'Review queues',
    items: [
      {
        title: 'Result reviews',
        url: '/result-reviews',
        icon: Gavel,
        permission: 'result_review.manage',
        count: (queues) => queues.resultReviews,
      },
      {
        title: 'Account verifications',
        url: '/verifications',
        icon: BadgeCheck,
        permission: 'game_account_verification.manage',
        count: (queues) => queues.accountVerifications,
      },
      {
        title: 'Conduct strikes',
        url: '/strikes',
        icon: ShieldAlert,
        permission: 'player_strike.revoke',
      },
    ],
  },
  {
    title: 'Payments',
    items: [
      {
        title: 'Payment reviews',
        url: '/payments',
        icon: Banknote,
        permission: 'payment_review.manage',
        count: (queues) => queues.paymentReviews,
      },
      {
        title: 'Refunds',
        url: '/refunds',
        icon: ReceiptText,
        permission: 'refund.view',
        count: (queues) => queues.refunds,
      },
    ],
  },
  {
    title: 'Administration',
    items: [
      {
        title: 'Staff & roles',
        url: '/staff',
        icon: UserCog,
        permission: 'staff.manage',
      },
    ],
  },
]

/** The signed-in staff member's navigation, with live queue counts as badges. */
export function useNavGroups(): NavGroup[] {
  const permissions = usePermissions()
  const { data: overview } = useOverview()
  return navigation
    .map((group) => ({
      title: group.title,
      items: group.items
        .filter((item) => permissions.includes(item.permission))
        .map(({ permission: _permission, count, ...item }) => {
          const queues: QueueCounts | undefined = overview && {
            ...overview.queues,
            paymentReviews: overview.finance?.paymentReviews ?? 0,
            refunds: overview.finance?.refunds ?? 0,
          }
          const value = queues && count ? count(queues) : 0
          return value > 0 ? { ...item, badge: String(value) } : item
        }),
    }))
    .filter((group) => group.items.length > 0)
}
